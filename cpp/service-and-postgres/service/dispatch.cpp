// Общий разбор запроса: одна маршрутизация на три слоя.
#include "dispatch.hpp"

#include <cstdlib>
#include <stdexcept>
#include <string>

namespace {

// Значение целочисленного параметра из строки запроса.
//
// Возвращает false, если параметра нет или он не целое число целиком.
// Запасного значения нет намеренно: проба, попросившая ожидание и
// получившая ноль из-за опечатки, вернулась бы мгновенно и записала бы в
// фикстуру успешный исход — замер при этом не состоялся бы вовсе.
bool int_param(const std::string& query_string, const std::string& key,
               int* out) {
    std::string::size_type pos = 0;
    while (pos <= query_string.size()) {
        const std::string::size_type amp = query_string.find('&', pos);
        const std::string pair = query_string.substr(
            pos, amp == std::string::npos ? std::string::npos : amp - pos);
        const std::string::size_type eq = pair.find('=');
        if (eq != std::string::npos && pair.substr(0, eq) == key) {
            const std::string value = pair.substr(eq + 1);
            if (value.empty()) return false;
            try {
                std::size_t consumed = 0;
                const int parsed = std::stoi(value, &consumed);
                if (consumed != value.size()) return false;
                *out = parsed;
                return true;
            } catch (const std::exception&) {
                return false;
            }
        }
        if (amp == std::string::npos) break;
        pos = amp + 1;
    }
    return false;
}

// Ответ на запрос, которого стенд не умеет: неизвестный путь, кривой
// параметр.
//
// ПОЧЕМУ ТЕЛО ЗДЕСЬ НЕ КОНТРАКТНАЯ СТРОКА. Контрактную строку печатают
// ручки, и она идёт в фикстуру. Ошибка ПРОБЫ — попросили не тот путь,
// перепутали имя параметра — обязана ронять разбор громко, а не ложиться
// в таблицу правильно оформленной строкой: «нет ни одной строки данных»
// заметно сразу, а лишняя строка с исходом error выглядит наблюдением.
Reply malformed(int status, const std::string& detail) {
    return Reply{status, "не запрос стенда: " + detail + "\n"};
}

}  // namespace

Reply dispatch(const std::string& target, const std::string& variant) {
    const std::string::size_type mark = target.find('?');
    const std::string path = target.substr(0, mark);
    const std::string query_string =
        mark == std::string::npos ? std::string() : target.substr(mark + 1);

    if (path == "/healthz") {
        // Имя варианта эта ручка берёт сама, из прочитанного при старте:
        // у контроля живости нет параметров, и передавать в него нечего.
        //
        // СТОРОЖА ЗДЕСЬ НЕТ НАМЕРЕННО. Этим маршрутом СПРАШИВАЮТ
        // занятость, и, посчитай он себя, ответ на пустом сервисе был бы
        // единицей — то есть занятость читалась бы всегда на один
        // больше, чем есть. Рабочую единицу эта ручка занимает как любой
        // другой запрос; счётчик же считает обработчиков ожидания, и
        // наблюдателю в этом счёте места нет.
        return healthz();
    }
    int value = 0;
    if (path == "/q") {
        if (!int_param(query_string, "sleep_ms", &value)) {
            return malformed(400, "/q без целого sleep_ms: " + query_string);
        }
        // Сторож ставится ЗДЕСЬ, в общем разборе, а не в трёх точках
        // входа: счёт обязан быть один на три варианта, иначе слои
        // различались бы ещё и способом счёта — то есть ровно тем, что
        // ось и меряет. Живёт он до возврата из обработчика, так что в
        // полёте числится всё время ожидания.
        InFlight guard;
        return query(value, variant, guard.entered_with());
    }
    if (path == "/block") {
        if (!int_param(query_string, "ms", &value)) {
            return malformed(400, "/block без целого ms: " + query_string);
        }
        InFlight guard;
        return block(value, variant, guard.entered_with());
    }
    return malformed(404, "неизвестный путь " + path);
}

int stand_workers() {
    static const int value = [] {
        const char* raw = std::getenv("STAND_WORKERS");
        if (raw == nullptr || *raw == '\0') {
            throw std::runtime_error(
                "переменная окружения STAND_WORKERS не задана: число рабочих "
                "единиц задаётся при запуске и запасного значения не имеет — "
                "дефолты у трёх библиотек разные, и оси мерили бы настройку");
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
                "STAND_WORKERS обязан быть положительным целым, получено: "
                + text);
        }
        return parsed;
    }();
    return value;
}
