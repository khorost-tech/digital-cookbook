// Сервис осей 3, 4 и 5. Маршруты:
//   /work?ms=N[&bytes=B] — ждёт N мс асинхронно (steady_timer, не sleep) и
//                     отвечает; bytes — длина набивки ответа (большой ответ);
//   /healthz        — отвечает сразу, в ответе — сколько /work в полёте;
//   /file?fail=0|1  — открывает файл; при fail=1 выходит из обработчика
//                     исключением посередине (ось 5).
// Ответ — строка контракта: {"kind":…,"inflight":…}.
//
// Переменные окружения:
//   STAND_SHUTDOWN        none | drain — ставить ли обработчик SIGTERM (ось 3)
//   STAND_DRAIN_MS        срок дослуживания после сигнала
//   STAND_CONFIG          naive | strict — способ чтения конфигурации (ось 4)
//   STAND_PORT, STAND_TIMEOUT_MS — сама конфигурация
//   STAND_FILE_OWNERSHIP  raw | raii — владение дескриптором (ось 5)
//   STAND_INFLIGHT_RELEASE after-ack | after-close | after-write | before-write —
//                         когда /work снимается с учёта (ось 3):
//                           after-ack    — очередь отправки сокета пуста: ядро
//                                          клиента подтвердило все байты и FIN;
//                           after-close  — чтение вернуло EOF: клиент закрыл
//                                          отправку — но, возможно, сразу после
//                                          запроса, а не после ответа;
//                           after-write  — async_write завершился: ответ сдан
//                                          ядру, хвост ещё в буфере сокета;
//                           before-write — ответ готов, отправка не начата.
//                         Ни один не значит «клиент прочитал»: это знает только
//                         протокол приложения. after-ack — граница, которую
//                         даёт транспорт: ответ в ядре клиента.
#include "config.hpp"

#include <boost/asio.hpp>

#include <array>
#include <atomic>
#include <chrono>
#include <csignal>
#include <cstdio>
#include <cerrno>
#include <cstdlib>
#include <cstring>
#include <fcntl.h>
#include <linux/sockios.h>
#include <sys/ioctl.h>
#include <functional>
#include <memory>
#include <stdexcept>
#include <string>
#include <unistd.h>

namespace asio = boost::asio;
using asio::ip::tcp;

namespace {

int in_flight = 0;                   // все обработчики — в одном потоке io_context
std::string ownership = "raii";
enum class Release { before_write, after_write, after_close, after_ack };
Release release_at = Release::after_ack;

std::string env_or(const char* name, const char* fallback) {
    const char* v = std::getenv(name);
    return v ? v : fallback;
}

int query_int(const std::string& target, const std::string& key, int fallback) {
    auto pos = target.find(key + "=");
    if (pos == std::string::npos) return fallback;
    return std::atoi(target.c_str() + pos + key.size() + 1);
}

std::string reply(int status, const std::string& body) {
    return "HTTP/1.1 " + std::to_string(status) + (status == 200 ? " OK" : " Error") +
           "\r\nContent-Type: application/json\r\nContent-Length: " + std::to_string(body.size()) +
           "\r\nConnection: close\r\n\r\n" + body;
}

// Ось 5: открыть файл и, при fail, выйти исключением до закрытия.
void touch_file(bool fail) {
    if (ownership == "raw") {
        int fd = ::open("/etc/hostname", O_RDONLY);
        if (fd < 0) throw std::runtime_error("open");
        if (fail) throw std::runtime_error("сбой посередине");   // close не выполнится
        ::close(fd);
    } else {
        auto closer = [](int* p) { if (*p >= 0) ::close(*p); delete p; };
        std::unique_ptr<int, decltype(closer)> fd(new int(::open("/etc/hostname", O_RDONLY)), closer);
        if (*fd < 0) throw std::runtime_error("open");
        if (fail) throw std::runtime_error("сбой посередине");   // deleter закроет
    }
}

class Session : public std::enable_shared_from_this<Session> {
public:
    explicit Session(tcp::socket s) : socket_(std::move(s)), timer_(socket_.get_executor()) {}

    void start() {
        auto self = shared_from_this();
        asio::async_read_until(socket_, buf_, "\r\n\r\n",
            [self](boost::system::error_code ec, std::size_t) { if (!ec) self->route(); });
    }

private:
    void route() {
        std::istream in(&buf_);
        std::string method, target;
        in >> method >> target;
        if (target.rfind("/work", 0) == 0) {
            ++in_flight;
            timer_.expires_after(std::chrono::milliseconds(query_int(target, "ms", 0)));
            auto self = shared_from_this();
            auto pad = static_cast<std::size_t>(query_int(target, "bytes", 0));
            timer_.async_wait([self, pad](boost::system::error_code) {
                std::string body = "{\"kind\":\"work\",\"inflight\":" + std::to_string(in_flight) +
                                   ",\"pad\":\"" + std::string(pad, 'x') + "\"}";
                if (release_at == Release::before_write) --in_flight;   // ответ ещё не отправлен
                self->send(reply(200, body), release_at != Release::before_write);
            });
        } else if (target.rfind("/healthz", 0) == 0) {
            send(reply(200, "{\"kind\":\"healthz\",\"inflight\":" + std::to_string(in_flight) + "}"));
        } else if (target.rfind("/file", 0) == 0) {
            try {
                touch_file(query_int(target, "fail", 0) != 0);
                send(reply(200, "{\"kind\":\"file\",\"ok\":true}"));
            } catch (const std::exception&) {
                send(reply(500, "{\"kind\":\"file\",\"ok\":false}"));
            }
        } else {
            send(reply(404, "{\"kind\":\"unknown\"}"));
        }
    }

    // release — /work, который ещё числится в полёте и снимается здесь.
    void send(std::string text, bool release = false) {
        auto self = shared_from_this();
        auto data = std::make_shared<std::string>(std::move(text));
        asio::async_write(socket_, asio::buffer(*data),
            [self, data, release](boost::system::error_code ec, std::size_t) {
                boost::system::error_code ignored;
                self->socket_.shutdown(tcp::socket::shutdown_send, ignored);   // FIN после ответа
                // Ошибка записи — конец жизни запроса, но не дослуживание.
                bool now = release && (ec || release_at == Release::after_write);
                if (now) --in_flight;                    // after-write: сдан ядру — ещё не доставлен
                if (ec) return;
                bool rest = release && !now;
                if (rest && release_at == Release::after_ack) self->await_ack();
                self->await_close(rest && release_at == Release::after_close);
            });
    }

    // after-close. Чтение до EOF или ошибки: клиент закрыл свою сторону.
    // EOF не значит «дочитал»: клиент мог закрыть отправку сразу после
    // запроса и читать ответ дальше. Ошибка тоже снимает запрос с учёта —
    // это конец его жизни, а не обязательно доставка.
    void await_close(bool release) {
        auto self = shared_from_this();
        socket_.async_read_some(asio::buffer(sink_),
            [self, release](boost::system::error_code ec, std::size_t) {
                if (!ec) { self->await_close(release); return; }
                if (release) --in_flight;       // клиент закрыл — дослужен
            });
    }

    // after-ack. Очередь отправки пуста (SIOCOUTQ == 0): ядро клиента
    // подтвердило все байты ответа и FIN. От поведения клиента не зависит;
    // что приложение клиента ответ прочитало, транспорт не знает.
    void await_ack() {
        int queued = 0;
        if (::ioctl(socket_.native_handle(), SIOCOUTQ, &queued) != 0) {
            // Состояние очереди неизвестно: конец жизни запроса с ошибкой,
            // а не подтверждение доставки.
            std::fprintf(stderr, "SIOCOUTQ: %s — запрос снят с учёта без подтверждения\n",
                         std::strerror(errno));
            --in_flight;
            return;
        }
        if (queued == 0) {
            --in_flight;                        // ответ в ядре клиента — дослужен
            return;
        }
        timer_.expires_after(std::chrono::milliseconds(10));
        auto self = shared_from_this();
        timer_.async_wait([self](boost::system::error_code) { self->await_ack(); });
    }

    tcp::socket socket_;
    asio::steady_timer timer_;
    asio::streambuf buf_;
    std::array<char, 256> sink_{};
};

void accept_loop(tcp::acceptor& acceptor) {
    acceptor.async_accept([&acceptor](boost::system::error_code ec, tcp::socket s) {
        if (ec) return;                                  // приём закрыт — цикл кончился
        std::make_shared<Session>(std::move(s))->start();
        accept_loop(acceptor);
    });
}

}  // namespace

int main() {
    std::setvbuf(stdout, nullptr, _IOLBF, 0);
    Config cfg{};
    if (env_or("STAND_CONFIG", "strict") == "naive") {
        cfg = read_config_naive();
    } else {
        auto strict = read_config_strict();
        if (!strict) {
            std::fprintf(stderr, "конфигурация: %s\n", strict.error().c_str());
            return 2;
        }
        cfg = *strict;
    }
    ownership = env_or("STAND_FILE_OWNERSHIP", "raii");
    std::string release = env_or("STAND_INFLIGHT_RELEASE", "after-ack");
    release_at = release == "before-write" ? Release::before_write
               : release == "after-write"  ? Release::after_write
               : release == "after-close"  ? Release::after_close
                                           : Release::after_ack;

    asio::io_context io;
    tcp::acceptor acceptor(io, tcp::endpoint(tcp::v4(), static_cast<unsigned short>(cfg.port)));
    std::printf("порт %d\n", acceptor.local_endpoint().port());
    accept_loop(acceptor);

    asio::signal_set signals(io);
    asio::steady_timer deadline(io);
    if (env_or("STAND_SHUTDOWN", "none") == "drain") {
        int drain_ms = std::atoi(env_or("STAND_DRAIN_MS", "5000").c_str());
        signals.add(SIGTERM);
        signals.async_wait([&](const boost::system::error_code& ec, int) {
            if (ec) return;
            boost::system::error_code ignored;
            acceptor.close(ignored);                     // новых не принимаем
            deadline.expires_after(std::chrono::milliseconds(drain_ms));
            deadline.async_wait([&](boost::system::error_code) { io.stop(); });   // срок вышел
            // Дослуживание: проверять, не опустел ли полёт, раз в 20 мс.
            auto poll = std::make_shared<std::function<void()>>();
            auto tick = std::make_shared<asio::steady_timer>(io);
            *poll = [&, poll, tick]() {
                if (in_flight == 0) { io.stop(); return; }
                tick->expires_after(std::chrono::milliseconds(20));
                tick->async_wait([poll](boost::system::error_code) { (*poll)(); });
            };
            (*poll)();
        });
    }
    io.run();
    return 0;
}
