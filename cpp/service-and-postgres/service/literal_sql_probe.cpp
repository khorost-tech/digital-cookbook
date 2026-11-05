// ОТРИЦАТЕЛЬНЫЙ КОНТРОЛЬ ОСИ 3: тот же клиент базы, но текст с величиной.
//
// ЗАЧЕМ ОН ЕСТЬ. Ось 3 утверждает, что параметризованный вызов видно
// снаружи: пока запрос выполняется, его текст целиком лежит в
// `pg_stat_activity`, и на месте величины стоит `$1`. Один лишь факт
// «текст такой-то» не доказывает ничего: нужен участник, у которого
// текст ДРУГОЙ, а всё остальное то же самое. Иначе «мы всегда видим
// параметр» может означать не свойство драйвера, а свойство нашего
// чтения.
//
// ПОЧЕМУ ЭТО ПРОБА, А НЕ ЧЕТВЁРТЫЙ ВАРИАНТ СЕРВИСА. Текст запроса живёт
// в ОБЩЕЙ логике сервиса (`main.cpp`), одной на три слоя. Ветка с
// литералом там изменила бы бинарник ВСЕХ трёх вариантов — а его сумма
// записана в фикстурах осей 1, 2 и 4 и сверяется свежей сборкой. То
// есть ради контроля оси 3 пришлось бы переснять три чужих замера, и
// числа там плавают между сессиями. Проба собирается отдельным
// бинарником ВНУТРИ образа варианта — ровно так же, как проба
// собственного пула Drogon (`drogon_pool_probe.cpp`), — и сервиса не
// касается вовсе.
//
// ЧТО ЭТА ПРОБА ДОКАЗЫВАЕТ: чтение из `pg_stat_activity` различает
// параметризованный текст и текст с вклеенной величиной, у того же
// клиента базы (libpqxx), в том же образе, на той же базе.
//
// ЧЕГО ОНА НЕ ДОКАЗЫВАЕТ: что сервис МОГ БЫ прислать литерал. Сервис
// шлёт параметр, и это его свойство, а не наше наблюдение о нём.
//
// Печатает «PROBE DONE» последней строкой: оборванная проба не должна
// выглядеть законченной.
#include <cstdlib>
#include <iomanip>
#include <iostream>
#include <sstream>
#include <stdexcept>
#include <string>

#include <pqxx/pqxx>

namespace {

std::string env_or(const char* name, const std::string& fallback) {
    const char* value = std::getenv(name);
    return (value != nullptr && *value != '\0') ? std::string(value) : fallback;
}

int env_int(const char* name, int fallback) {
    const std::string text = env_or(name, "");
    if (text.empty()) return fallback;
    return std::stoi(text);
}

// ТЕКСТ С ВКЛЕЕННОЙ ВЕЛИЧИНОЙ. Здесь это ПРЕДМЕТ, а не дефект: в сервисе
// подстановка величины в текст запрещена и проверяется, и запрет там
// остаётся в силе.
std::string literal_sleep_sql(double seconds) {
    std::ostringstream text;
    text << "SELECT pg_sleep(" << std::fixed << std::setprecision(3)
         << seconds << ")";
    return text.str();
}

}  // namespace

int main() {
    const std::string dsn = env_or("STAND_DSN", "");
    const std::string name = env_or("STAND_APPLICATION_NAME", "");
    if (dsn.empty() || name.empty()) {
        std::cerr << "STAND_DSN и STAND_APPLICATION_NAME обязательны: проба "
                     "без имени приложения считалась бы чужими соединениями"
                  << std::endl;
        return 2;
    }
    const int sleep_ms = env_int("STAND_SLEEP_MS", 1500);
    const int requests = env_int("STAND_REQUESTS", 4);
    try {
        // Имя приложения — в строке подключения, как и у сервиса: по нему
        // ось отличает свои строки от чужих.
        pqxx::connection connection(dsn + " application_name='" + name + "'");
        for (int index = 0; index < requests; ++index) {
            pqxx::work tx(connection);
            tx.exec(literal_sleep_sql(static_cast<double>(sleep_ms) / 1000.0));
            tx.commit();
        }
    } catch (const std::exception& failure) {
        std::cerr << "проба не выполнила запрос: " << failure.what()
                  << std::endl;
        return 1;
    }
    std::cout << "PROBE DONE" << std::endl;
    return 0;
}
