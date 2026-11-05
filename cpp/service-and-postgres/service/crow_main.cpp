// Точка входа слоя Crow.
//
// ЧТО ЗДЕСЬ ЕСТЬ И ЧЕГО НЕТ. Только слой: принять запрос, отдать его
// строку общему разбору (dispatch), вернуть полученный ответ. Ни
// маршрутизации по путям, ни разбора параметров — у Crow для этого есть
// собственные средства, и воспользоваться ими значило бы развести три
// варианта ещё и разбором запроса. Здесь заведён ОДИН перехватывающий
// маршрут, и строка запроса уходит в общий код как есть.
//
// РАБОЧАЯ ЕДИНИЦА У ЭТОГО СЛОЯ — ПОТОК ИЗ ПУЛА, задаваемый вызовом
// concurrency(). Дефолт Crow — число аппаратных потоков машины, то есть
// величина, зависящая от места запуска. Число приходит из STAND_WORKERS
// при запуске.
#include <cstdint>
#include <exception>
#include <iostream>
#include <string>

#include "crow_all.h"

#include "dispatch.hpp"
#include "handlers.hpp"

namespace {

// Порт один и тот же у трёх слоёв: обвязка стенда обращается к варианту
// по одному адресу, и различие в порте было бы различием обвязки.
constexpr std::uint16_t kPort = 8080;

}  // namespace

int main() {
    try {
        // Имя варианта и число рабочих единиц читаются ЗДЕСЬ, до
        // открытия порта. Ошибка настройки обязана быть видна как
        // закрытый порт: отказ из обработчика ось 1 прочла бы как
        // отсутствие живости, то есть испортила бы главную ось.
        const std::string variant = stand_variant();
        const int workers = stand_workers();
        // Размер пула соединений читается ЗДЕСЬ ЖЕ и по той же
        // причине: пул создаётся лениво, при первом походе в базу, и
        // ошибка настройки вылезла бы отказом одного запроса — то
        // есть выглядела бы отказом базы, а не испорченным запуском.
        stand_pool_size();

        crow::SimpleApp app;
        // Журнал приглушён: под нагрузкой построчная печать каждого
        // запроса сама становится замеряемой величиной, и у трёх слоёв
        // она разная.
        app.loglevel(crow::LogLevel::Warning);

        CROW_CATCHALL_ROUTE(app)
        ([&variant](const crow::request& request, crow::response& response) {
            const Reply reply = dispatch(request.raw_url, variant);
            response.code = reply.status;
            response.set_header("Content-Type", "application/json");
            response.body = reply.body;
            response.end();
        });

        app.port(kPort)
            .concurrency(static_cast<std::uint16_t>(workers))
            .run();
    } catch (const std::exception& failure) {
        std::cerr << "crow: запуск не состоялся: " << failure.what()
                  << std::endl;
        return 1;
    }
    return 0;
}
