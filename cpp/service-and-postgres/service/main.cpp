// Общая логика сервиса стенда: три ручки и ничего лишнего.
//
// ПОЧЕМУ ЗДЕСЬ НЕТ main(). Слоёв HTTP три, и различаться обязан ТОЛЬКО
// слой: один компилятор, одна версия libpqxx, одна схема, один и тот же
// текст запроса. Поэтому логика ручек живёт в одном месте и собирается в
// библиотеку, а каждый слой добавляет свою точку входа. В волне A пара
// различалась образом сборки целиком, вместе с компилятором, и причинную
// трактовку пришлось отзывать уже после написания статей.
//
// ЧЕГО ЗДЕСЬ НЕТ: маршрутизации сложнее трёх ручек, JSON сверх
// контрактной строки, TLS, ORM, шаблонов. Каждое из этого добавило бы
// переменных, не относящихся к осям.
#include "handlers.hpp"

#include "pool.hpp"

#include <atomic>
#include <chrono>
#include <cstddef>
#include <cstdlib>
#include <memory>
#include <stdexcept>
#include <string>
#include <thread>

#include <pqxx/pqxx>

namespace {

// Верхняя граница ожидания. Проба, попросившая ожидание больше этого,
// почти наверняка ошиблась размерностью (секунды вместо миллисекунд), и
// прогон встал бы на час. Отказ с объяснением лучше зависания: зависание
// неотличимо от занятого сервера, а ось 1 меряет ровно это различие.
constexpr int kMaxWaitMs = 60000;

// Коды состояния PostgreSQL, при которых отказала БАЗА, а не наша проба.
//
// ПРАВИЛО, ЗАПИСАННОЕ ДО ПЕРВОГО ПРОГОНА. «FATAL: sorry, too many clients
// already» — это свойство общего ресурса, и на оси 2, которая считает
// соединения, он окажется самым интересным исходом из возможных.
// Записать его в error значило бы спрятать находку в графу «стенд
// сломался», откуда её никто не достанет. Поэтому:
//
//   refused — база отказала обслуживать: исчерпаны соединения (53300),
//             база не принимает подключения (57P03), соединение разорвано
//             на стороне сервера. Это наблюдение, и оно идёт в таблицу;
//   error   — всё остальное: синтаксис запроса, отсутствующая роль,
//             нехватка прав. Это поломка НАШЕЙ пробы, и её надо чинить,
//             а не печатать как свойство фреймворка.
//
// Список кодов пополняется только вместе с объяснением, почему очередной
// код — отказ общего ресурса, а не наша ошибка.
constexpr const char* kTooManyConnections = "53300";
constexpr const char* kCannotConnectNow = "57P03";

// Экранирование для JSON-строк вручную собираемого контракта. Значения
// (имя варианта, текст ошибки базы) приходят снаружи, и кавычка внутри
// значения молча оборвала бы строку. Такая поломка выглядела бы как сбой
// пробы, а на деле была бы дефектом сериализации здесь.
std::string json_escape(const std::string& value) {
    static const char* kHex = "0123456789abcdef";
    std::string out;
    out.reserve(value.size());
    for (unsigned char ch : value) {
        switch (ch) {
            case '"': out += "\\\""; break;
            case '\\': out += "\\\\"; break;
            case '\n': out += "\\n"; break;
            case '\r': out += "\\r"; break;
            case '\t': out += "\\t"; break;
            default:
                if (ch < 0x20) {
                    out += "\\u00";
                    out += kHex[(ch >> 4) & 0x0f];
                    out += kHex[ch & 0x0f];
                } else {
                    out += static_cast<char>(ch);
                }
        }
    }
    return out;
}

// Счётчик обработчиков в полёте. Живёт в общей логике, а не в слое: он
// ОДИН на три варианта, иначе три слоя считали бы тремя способами и
// различались бы не только слоем HTTP.
std::atomic<int> g_inflight{0};

// Строка контракта: пять полей волны A плюс ШЕСТОЕ — прочитанная
// занятость. Исходов три, и третий отделён намеренно: «наша проба
// сломалась» не должна попадать в таблицу как свойство фреймворка.
//
// ПОЧЕМУ ЗАНЯТОСТЬ ЕДЕТ В ТОЙ ЖЕ СТРОКЕ. Отдельная ручка статистики
// отвечала бы на свой собственный запрос в своё собственное время, и
// связать её ответ с конкретной пробой было бы нечем. Здесь число
// приезжает вместе с ответом той пробы, которая его и создала.
std::string json_line(const std::string& variant, const char* probe,
                      const char* outcome, const std::string& detail,
                      int inflight) {
    return std::string("{\"kind\":\"service\",\"variant\":\"")
           + json_escape(variant) + "\",\"probe\":\"" + probe
           + "\",\"outcome\":\"" + outcome + "\",\"inflight\":"
           + std::to_string(inflight) + ",\"detail\":\""
           + json_escape(detail) + "\"}";
}

std::string env_or(const char* name, const std::string& fallback) {
    const char* value = std::getenv(name);
    return (value != nullptr && *value != '\0') ? std::string(value) : fallback;
}

// Экранирование значения для строки подключения libpq в формате
// «ключ=значение»: одинарные кавычки и обратные косые внутри значения
// обязаны быть закрыты, иначе имя варианта с лишним символом порвало бы
// строку подключения, и сервис молча подключился бы без
// application_name — а по нему оси отличают свои соединения от чужих.
std::string dsn_quote(const std::string& value) {
    std::string out = "'";
    for (char ch : value) {
        if (ch == '\'' || ch == '\\') out += '\\';
        out += ch;
    }
    out += "'";
    return out;
}

// Сверка имени, полученного ручкой, с прочитанным при старте.
//
// ЗАЧЕМ, ЕСЛИ ИМЯ УЖЕ ЗАЩИЩЕНО У ИСТОЧНИКА. Защита у источника не мешает
// слою передать в ручку литерал или пустую строку — и тогда фикстура
// заполнится чужим именем, а прогон будет выглядеть успешным. Здесь
// расхождение не переживает первого же запроса.
//
// Это ошибка программиста слоя, а не настройки: она детерминирована,
// видна на первом запросе при разработке слоя и не может возникнуть от
// окружения. Имя же, пришедшее из окружения, проверено при старте, до
// открытия порта, — так что живость эта сверка не портит.
void require_variant(const std::string& variant) {
    if (variant != stand_variant()) {
        throw std::logic_error(
            "ручке передано имя варианта, отличное от прочитанного при "
            "старте: " + variant);
    }
}

// ОТКРЫТИЕ ОДНОГО СОЕДИНЕНИЯ. Зовётся не обработчиком, а пулом — и
// только когда в наборе нет свободного и есть незанятый слот.
//
// ДО ОСИ 2 ЭТО ЗВАЛОСЬ НА КАЖДЫЙ ЗАПРОС, у всех трёх слоёв, и замер это
// подтверждал: во время залпа соединений было ровно столько, сколько
// запросов было В ПОЛЁТЕ (а не сколько рабочих единиц: у слоя с меньшим
// потолком их меньше), а после залпа — ноль. Пул появился вместе с осью,
// которая его меряет, и он ОДИН на три слоя: пул, написанный для
// каждого слоя своими средствами, развёл бы варианты ещё и клиентом
// базы (см. pool.hpp).
std::unique_ptr<pqxx::connection> connect(const std::string& variant) {
    // application_name обязан равняться имени варианта: база — общий
    // ресурс, и без этого варианты видели бы чужие соединения.
    //
    // Пароль в строке по умолчанию не случаен: роль в schema.sql заведена
    // с паролем, и строка без него работала бы только в режиме trust —
    // первый же прогон на обычной базе упал бы на соединении, и упал бы
    // исходом refused, то есть выглядел бы как отказ общего ресурса.
    const std::string dsn =
        env_or("STAND_DSN",
               "host=postgres port=5432 dbname=stand user=stand "
               "password=stand")
        + " application_name=" + dsn_quote(variant);
    return std::make_unique<pqxx::connection>(dsn);
}

// НАБОР СОЕДИНЕНИЙ ВАРИАНТА — один на процесс.
//
// ПОЧЕМУ СТАТИЧЕСКИЙ, А НЕ ПОЛЕ ЧЕГО-НИБУДЬ. Три слоя устроены
// по-разному: у одного обработчик живёт в корутине, у другого в потоке
// пула, у третьего на цикле событий. Общего места, где слои могли бы
// передать пул в ручку, нет — а заводить его в трёх точках входа значило
// бы завести ТРИ РАЗНЫХ пула и мерить потом их различия.
//
// РАЗМЕР ЧИТАЕТСЯ ОДИН РАЗ, из STAND_POOL, и слой обязан позвать
// stand_pool_size() ДО ОТКРЫТИЯ ПОРТА: иначе ошибка настройки вылезла бы
// первым запросом, то есть выглядела бы отказом базы.
ConnectionPool<pqxx::connection>& connections() {
    static ConnectionPool<pqxx::connection> pool(
        static_cast<std::size_t>(stand_pool_size()),
        [] { return connect(stand_variant()); });
    return pool;
}

}  // namespace

bool is_resource_refusal(const std::string& sqlstate) {
    // Список закрыт и пополняется только вместе с объяснением, почему
    // очередной код — отказ общего ресурса, а не наша ошибка. Всё, чего
    // здесь нет, — наша поломка: синтаксис запроса, отсутствующая роль,
    // нехватка прав. Такой ответ «по умолчанию» выбран намеренно:
    // ошибиться в сторону «стенд сломался» безопасно, ошибиться в
    // сторону «так ведёт себя база» — значит напечатать находку, которой
    // не было.
    return sqlstate == kTooManyConnections || sqlstate == kCannotConnectNow;
}

const std::string& stand_variant() {
    static const std::string value = [] {
        const char* raw = std::getenv("STAND_VARIANT");
        if (raw == nullptr || *raw == '\0') {
            throw std::runtime_error(
                "переменная окружения STAND_VARIANT не задана: имя варианта "
                "передаётся при запуске и запасного значения не имеет");
        }
        return std::string(raw);
    }();
    return value;
}

int stand_pool_size() {
    static const int value = [] {
        const char* raw = std::getenv("STAND_POOL");
        if (raw == nullptr || *raw == '\0') {
            throw std::runtime_error(
                "переменная окружения STAND_POOL не задана: размер пула "
                "соединений задаётся при запуске и запасного значения не "
                "имеет — запечённая величина склеила бы две настройки оси 2 "
                "в одну строку фикстуры, и контроль сравнивал бы пул сам с "
                "собой");
        }
        const std::string text(raw);
        std::size_t consumed = 0;
        int parsed = 0;
        try {
            parsed = std::stoi(text, &consumed);
        } catch (const std::exception&) {
            consumed = 0;
        }
        if (consumed != text.size() || parsed <= 0) {
            throw std::runtime_error(
                "STAND_POOL обязан быть положительным целым, получено: "
                + text);
        }
        return parsed;
    }();
    return value;
}

int inflight_now() { return g_inflight.load(std::memory_order_relaxed); }

InFlight::InFlight()
    : entered_(g_inflight.fetch_add(1, std::memory_order_relaxed) + 1) {}

InFlight::~InFlight() { g_inflight.fetch_sub(1, std::memory_order_relaxed); }

Reply healthz() {
    // В базу не ходим намеренно. Эта ручка отвечает на вопрос «сервер жив
    // или занят», и обращение к базе подмешало бы к ответу состояние
    // общего ресурса: занятая база читалась бы как мёртвый сервер.
    //
    // СЕБЯ ЭТА РУЧКА НЕ СЧИТАЕТ, и это не мелочь оформления: занятость
    // спрашивают именно ею, и в ответ должно приезжать число ЗАНЯТЫХ
    // обработчиков, а не оно же плюс спрашивающий. На пустом сервисе
    // ответ обязан быть нулём — это и есть проверяемое обещание.
    return Reply{200, json_line(stand_variant(), "healthz", "served", "200",
                                inflight_now())};
}

Reply query(int sleep_ms, const std::string& variant, int inflight) {
    require_variant(variant);
    if (sleep_ms < 0 || sleep_ms > kMaxWaitMs) {
        return Reply{400, json_line(variant, "q", "refused",
                                    "sleep_ms вне допустимых границ: "
                                        + std::to_string(sleep_ms), inflight)};
    }
    try {
        // СОЕДИНЕНИЕ БЕРЁТСЯ ИЗ ПУЛА и возвращается в него сторожем — в
        // том числе при выходе исключением (см. pool.hpp, Lease). Если
        // свободного слота нет, обработчик ЖДЁТ его здесь же, занимая
        // рабочую единицу: ровно так же, как он занимает её ожиданием в
        // базе. Это записано числом в фикстуре оси 2 (занятость при пуле
        // на 1), а не спрятано.
        //
        // ВЗЯТИЕ СТОИТ ВНУТРИ try НАМЕРЕННО: подключение — тоже поход в
        // сеть, и отказ на нём обязан приехать контрактной строкой с
        // исходом refused, а не выпасть исключением в слой. Выпавшее
        // исключение слой печатает по-своему, и три варианта ответили бы
        // на один и тот же отказ базы тремя разными способами.
        ConnectionPool<pqxx::connection>::Lease lease =
            connections().acquire();
        try {
            pqxx::work tx(*lease);
            // Величина идёт ПАРАМЕТРОМ, а не подстановкой в текст. Подстановка
            // сделала бы каждый вызов новым текстом запроса: ось 3 считала бы
            // вызовы разных запросов, а подготавливать было бы нечего.
            //
            // Приведения типа у параметра нет намеренно. У функции ожидания
            // одна сигнатура, libpqxx шлёт параметр нетипизированным, и сервер
            // разрешает его по единственному кандидату. Приведение изменило бы
            // ТЕКСТ запроса, который ось 3 читает из pg_stat_statements, а
            // текст замораживается один раз на живой базе в задаче 3.
            tx.exec_params("SELECT pg_sleep($1)",
                           static_cast<double>(sleep_ms) / 1000.0);
            tx.commit();
        } catch (...) {
            // СОЕДИНЕНИЕ, НА КОТОРОМ ЧТО-ТО ОТКАЗАЛО, В НАБОР НЕ
            // ВОЗВРАЩАЕТСЯ. Порванное соединение, вернувшееся в пул,
            // отравило бы каждый следующий запрос, взявший тот же слот:
            // замер видел бы череду отказов там, где отказала одна
            // попытка, — и списал бы это на слой. Слот при этом
            // освобождается, так что набор не усыхает.
            lease.discard();
            throw;
        }
    } catch (const pqxx::broken_connection& failure) {
        // Соединение не состоялось или разорвано сервером. Исчерпанные
        // соединения приходят сюда же: libpq отвечает отказом ещё до
        // сеанса, и SQLSTATE в исключении может не доехать.
        return Reply{503, json_line(variant, "q", "refused",
                                    std::string("broken_connection: ")
                                        + failure.what(), inflight)};
    } catch (const pqxx::sql_error& failure) {
        const std::string state = failure.sqlstate();
        const bool resource_refused = is_resource_refusal(state);
        return Reply{resource_refused ? 503 : 500,
                     json_line(variant, "q",
                               resource_refused ? "refused" : "error",
                               "SQLSTATE " + state + ": " + failure.what(), inflight)};
    } catch (const std::exception& failure) {
        // Исход error: сервис не отказывал — не сработала наша проба, и в
        // таблицу оси это идти не должно.
        return Reply{500, json_line(variant, "q", "error", failure.what(), inflight)};
    }
    return Reply{200, json_line(variant, "q", "served",
                                "sleep_ms=" + std::to_string(sleep_ms), inflight)};
}

Reply block(int ms, const std::string& variant, int inflight) {
    require_variant(variant);
    if (ms < 0 || ms > kMaxWaitMs) {
        return Reply{400, json_line(variant, "block", "refused",
                                    "ms вне допустимых границ: "
                                        + std::to_string(ms), inflight)};
    }
    // Ожидание ЗДЕСЬ, в обработчике, а не в базе. Рабочая единица на это
    // время занята, и ровно это различие меряет ось 1.
    std::this_thread::sleep_for(std::chrono::milliseconds(ms));
    return Reply{200, json_line(variant, "block", "served",
                                "ms=" + std::to_string(ms), inflight)};
}
