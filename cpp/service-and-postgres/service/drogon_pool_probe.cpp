// Проба СОБСТВЕННОГО пула Drogon — отдельно от оси и не вместо неё.
//
// ЗАЧЕМ ОНА ЕСТЬ. Спека волны предвидела асимметрию: «у Drogon пул свой,
// у Beast и Crow его нет — писать руками». Проба до прогона показала, что
// в этом стенде пула не было НИ У КОГО: соединение открывалось на каждый
// запрос у всех трёх слоёв. Значит асимметрия — не наблюдение, а
// ожидание, и утверждать её без замера нельзя. Здесь она замеряется.
//
// ПОЧЕМУ ЭТО НЕ ЧАСТЬ ОСИ. Собственный пул Drogon ходит в базу через
// libpq САМОГО ФРЕЙМВОРКА, а три варианта оси — через общий для них
// libpqxx. Поставь мы эту строку в одну таблицу с ними — ось сравнивала
// бы драйверы, а не слои HTTP, и рухнуло бы главное правило стенда.
// Поэтому строка фикстуры помечена used_in_axis: false, а сравнение
// идёт по одному признаку: пошло ли число соединений за СВОЕЙ настройкой.
//
// ЧТО ОНА ДЕЛАЕТ. Заводит DbClient с заданным числом соединений,
// отправляет столько же одновременных запросов с ожиданием в базе и
// держит клиента живым, пока снаружи считают соединения в
// pg_stat_activity. Число соединений СЧИТАЕТСЯ СНАРУЖИ, из базы, — то же
// правило, что у оси: объявление драйвера тут ничего не решает.
//
// Настройки приходят окружением: STAND_POOL (число соединений клиента),
// STAND_DSN (адрес базы вместе с application_name), STAND_CONCURRENT
// (сколько запросов слать разом), STAND_SLEEP_MS (ожидание в базе).
#include <cstdio>
#include <cstdlib>
#include <exception>
#include <future>
#include <string>
#include <vector>

#include <drogon/orm/DbClient.h>

namespace {

std::string required(const char* name) {
    const char* raw = std::getenv(name);
    if (raw == nullptr || *raw == '\0') {
        throw std::runtime_error(
            std::string("переменная окружения ") + name
            + " не задана: проба собственного пула не подставляет запасных "
              "значений — замер с неизвестной настройкой не замер");
    }
    return std::string(raw);
}

int required_number(const char* name) {
    const std::string text = required(name);
    std::size_t consumed = 0;
    const int value = std::stoi(text, &consumed);
    if (consumed != text.size() || value <= 0) {
        throw std::runtime_error(std::string(name)
                                 + " обязан быть положительным целым: "
                                 + text);
    }
    return value;
}

}  // namespace

int main() {
    try {
        const std::string dsn = required("STAND_DSN");
        const int size = required_number("STAND_POOL");
        const int concurrent = required_number("STAND_CONCURRENT");
        const double sleep_s = required_number("STAND_SLEEP_MS") / 1000.0;

        // ЧИСЛО СОЕДИНЕНИЙ — ВТОРОЙ ДОВОД newPgClient. Это и есть ручка
        // собственного пула фреймворка; ради неё проба и написана.
        std::shared_ptr<drogon::orm::DbClient> client =
            drogon::orm::DbClient::newPgClient(dsn, size);

        std::vector<std::future<drogon::orm::Result>> pending;
        pending.reserve(concurrent);
        for (int index = 0; index < concurrent; ++index) {
            pending.push_back(
                client->execSqlAsyncFuture("SELECT pg_sleep($1)", sleep_s));
        }
        // Готовность печатается ПОСЛЕ отправки залпа и до его окончания:
        // снаружи по этой строке узнаю́т, что считать соединения пора.
        std::printf("PROBE BUSY\n");
        std::fflush(stdout);
        int served = 0;
        for (std::future<drogon::orm::Result>& one : pending) {
            try {
                one.get();
                ++served;
            } catch (const std::exception& failure) {
                std::fprintf(stderr, "запрос не обслужен: %s\n",
                             failure.what());
            }
        }
        std::printf("PROBE DONE served=%d of %d\n", served, concurrent);
        std::fflush(stdout);
        return served == concurrent ? 0 : 1;
    } catch (const std::exception& failure) {
        std::fprintf(stderr, "проба собственного пула не состоялась: %s\n",
                     failure.what());
        return 2;
    }
}
