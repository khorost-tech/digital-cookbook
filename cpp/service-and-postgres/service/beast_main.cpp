// Точка входа слоя Boost.Beast — КОРУТИНЫ C++20 поверх Asio.
//
// ПОЧЕМУ ИМЕННО КОРУТИНЫ. Этот вариант представляет в серии модель
// «awaitable + co_spawn», и статья волны — про неё. Синхронная обработка
// в потоке дала бы ту же таблицу чисел, но про другую модель: читатель
// сравнивал бы с Drogon и Crow не то, что ему названо.
//
// ЧЕГО КОРУТИНЫ ЗДЕСЬ НЕ ДЕЛАЮТ, И ЭТО ГЛАВНОЕ. Асинхронны здесь чтение
// запроса и запись ответа — и только они. Ручка /block спит В
// ОБРАБОТЧИКЕ намеренно: ось 1 меряет занятость рабочей единицы.
//
// ЧТО ЗАМЕРЕНО ПРО ОЖИДАНИЕ В БАЗЕ (fixtures/db-wait.jsonl): при ОДНОЙ
// рабочей единице две одновременные пробы /q в одно ожидание не
// укладываются — вторая дожидается первой. Ожидание в базе рабочую
// единицу занимает, и корутина этого не меняет.
//
// ОБЪЯСНЕНИЕ, КОТОРОЕ СТЕНД НЕ ПРОВЕРЯЛ: клиент базы работает синхронно,
// и приостанавливаться корутине на времени запроса не на чем. Это
// правдоподобно и согласуется с замером, но замером не является — снято
// следствие, а не механизм. Разницу приходится называть вслух: в волне A
// причинную трактовку пришлось отзывать уже после выхода статей.
//
// ЧТО ЗДЕСЬ ЕСТЬ И ЧЕГО НЕТ. Только слой: принять соединение, прочитать
// запрос, отдать его строку общему разбору (dispatch), записать ответ. Ни
// маршрутизации, ни разбора параметров, ни похода в базу — всё это общее
// для трёх вариантов и живёт в stand_common.
//
// РАБОЧАЯ ЕДИНИЦА У ЭТОГО СЛОЯ — ПОТОК, КРУТЯЩИЙ io_context. Дефолта у
// Beast нет вовсе: сколько потоков заведёшь, столько и будет. Число
// приходит из STAND_WORKERS при запуске.
#include <boost/asio/awaitable.hpp>
#include <boost/asio/co_spawn.hpp>
#include <boost/asio/detached.hpp>
#include <boost/asio/io_context.hpp>
#include <boost/asio/ip/tcp.hpp>
#include <boost/asio/socket_base.hpp>
#include <boost/asio/use_awaitable.hpp>
#include <boost/beast/core.hpp>
#include <boost/beast/http.hpp>

#include <exception>
#include <iostream>
#include <string>
#include <thread>
#include <utility>
#include <vector>

#include "dispatch.hpp"
#include "handlers.hpp"

namespace beast = boost::beast;
namespace http = beast::http;
namespace asio = boost::asio;
using tcp = asio::ip::tcp;

namespace {

// Порт один и тот же у трёх слоёв: обвязка стенда обращается к варианту
// по одному адресу, и различие в порте было бы различием обвязки.
constexpr unsigned short kPort = 8080;

// Имя варианта передаётся КОПИЕЙ, а не ссылкой. Корутина переживает
// вызов, который её породил, и ссылка на чужой объект — обычный способ
// получить висячую ссылку, которая проявится только под нагрузкой.
asio::awaitable<void> serve(tcp::socket socket, std::string variant) {
    try {
        beast::flat_buffer buffer;
        http::request<http::string_body> request;
        co_await http::async_read(socket, buffer, request,
                                  asio::use_awaitable);

        // Ожидание внутри ручки остаётся синхронным намеренно: /block
        // занимает рабочую единицу, и ось 1 меряет ровно это.
        const Reply reply = dispatch(std::string(request.target()), variant);

        http::response<http::string_body> response{
            static_cast<http::status>(reply.status), request.version()};
        response.set(http::field::content_type, "application/json");
        // Соединение закрывается после ответа: держать его открытым
        // значило бы мерить ещё и повторное использование соединений,
        // которое у трёх библиотек устроено по-разному.
        response.keep_alive(false);
        response.body() = reply.body;
        response.prepare_payload();
        co_await http::async_write(socket, response, asio::use_awaitable);

        beast::error_code ignored;
        socket.shutdown(tcp::socket::shutdown_send, ignored);
    } catch (const std::exception& failure) {
        // Оборванное соединение — обычное дело под нагрузкой. Без перехвата
        // исключение ушло бы в обработчик завершения co_spawn, а detached
        // его молча отбрасывает: сбой соединения пропал бы без следа.
        // Перехват нужен ради строки в журнале, процесс он не спасает.
        std::cerr << "beast: соединение не обслужено: " << failure.what()
                  << std::endl;
    }
}

asio::awaitable<void> accept_loop(tcp::acceptor& acceptor,
                                  std::string variant) {
    for (;;) {
        tcp::socket socket =
            co_await acceptor.async_accept(asio::use_awaitable);
        // Обслуживание уходит ОТДЕЛЬНОЙ корутиной, а приём продолжается
        // сразу. Обслужи мы соединение здесь же, следующее принималось бы
        // только после ответа на текущее — и вариант выглядел бы
        // однопоточным независимо от числа рабочих единиц.
        asio::co_spawn(acceptor.get_executor(),
                       serve(std::move(socket), variant), asio::detached);
    }
}

}  // namespace

int main() {
    try {
        // Имя варианта и число рабочих единиц читаются ЗДЕСЬ, до
        // открытия порта, и отказ доезжает до кода возврата. Ошибка
        // настройки обязана быть видна как закрытый порт: отказ из
        // обработчика ось 1 прочла бы как отсутствие живости, то есть
        // испортила бы главную ось.
        const std::string variant = stand_variant();
        const int workers = stand_workers();
        // Размер пула соединений читается ЗДЕСЬ ЖЕ и по той же
        // причине: пул создаётся лениво, при первом походе в базу, и
        // ошибка настройки вылезла бы отказом одного запроса — то
        // есть выглядела бы отказом базы, а не испорченным запуском.
        stand_pool_size();

        asio::io_context context(workers);
        tcp::acceptor acceptor(context);
        const tcp::endpoint endpoint(tcp::v4(), kPort);
        acceptor.open(endpoint.protocol());
        acceptor.set_option(asio::socket_base::reuse_address(true));
        acceptor.bind(endpoint);
        acceptor.listen(asio::socket_base::max_listen_connections);
        asio::co_spawn(context, accept_loop(acceptor, variant),
                       asio::detached);

        std::vector<std::thread> threads;
        threads.reserve(workers);
        for (int index = 0; index < workers; ++index) {
            threads.emplace_back([&context] { context.run(); });
        }
        for (std::thread& thread : threads) {
            thread.join();
        }
    } catch (const std::exception& failure) {
        std::cerr << "beast: запуск не состоялся: " << failure.what()
                  << std::endl;
        return 1;
    }
    return 0;
}
