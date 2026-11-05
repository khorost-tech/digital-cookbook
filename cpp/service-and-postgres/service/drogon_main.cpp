// Точка входа слоя Drogon.
//
// ЧТО ЗДЕСЬ ЕСТЬ И ЧЕГО НЕТ. Только слой: принять запрос, отдать его
// строку общему разбору (dispatch), вернуть полученный ответ. Ни
// контроллеров, ни маршрутизации по путям, ни клиента базы самого Drogon
// — всё это увело бы вариант в сторону от двух остальных, и различался
// бы не только слой HTTP.
//
// РАБОЧАЯ ЕДИНИЦА У ЭТОГО СЛОЯ — ЦИКЛ СОБЫТИЙ, задаваемый вызовом
// setThreadNum(). Дефолт Drogon — единица (один цикл событий), и ровно
// поэтому число задаётся явно: без этого вариант сравнивался бы с
// соседями при другом числе рабочих единиц, а расхождение объяснялось бы
// двумя причинами сразу. Число приходит из STAND_WORKERS при запуске.
#include <exception>
#include <functional>
#include <iostream>
#include <string>

#include <drogon/drogon.h>

#include "dispatch.hpp"
#include "handlers.hpp"

namespace {

// Порт один и тот же у трёх слоёв: обвязка стенда обращается к варианту
// по одному адресу, и различие в порте было бы различием обвязки.
constexpr unsigned short kPort = 8080;

// Строка запроса в том виде, в каком её видят два других слоя: путь и, если
// параметры есть, знак вопроса с ними. Собирается здесь, а не в общем коде,
// потому что это свойство слоя — Drogon отдаёт путь и параметры порознь.
std::string target_of(const drogon::HttpRequestPtr& request) {
    const std::string path = request->path();
    const std::string parameters = request->query();
    return parameters.empty() ? path : path + "?" + parameters;
}

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

        drogon::app().setDefaultHandler(
            [variant](const drogon::HttpRequestPtr& request,
                      std::function<void(const drogon::HttpResponsePtr&)>&&
                          callback) {
                const Reply reply = dispatch(target_of(request), variant);
                const drogon::HttpResponsePtr response =
                    drogon::HttpResponse::newHttpResponse();
                response->setStatusCode(
                    static_cast<drogon::HttpStatusCode>(reply.status));
                response->setContentTypeCode(drogon::CT_APPLICATION_JSON);
                response->setBody(reply.body);
                callback(response);
            });
        // Журнал приглушён: под нагрузкой построчная печать каждого
        // запроса сама становится замеряемой величиной, и у трёх слоёв
        // она разная.
        drogon::app().setLogLevel(trantor::Logger::kWarn);
        drogon::app()
            .setThreadNum(workers)
            .addListener("0.0.0.0", kPort)
            .run();
    } catch (const std::exception& failure) {
        std::cerr << "drogon: запуск не состоялся: " << failure.what()
                  << std::endl;
        return 1;
    }
    return 0;
}
