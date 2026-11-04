// Минимальный сервис стенда: две ручки и ничего лишнего.
//
// ПОЧЕМУ БЕЗ ФРЕЙМВОРКА. HTTP-фреймворки — предмет отдельной статьи
// серии. Здесь мерится упаковка, и любая лишняя зависимость меняла бы
// сразу две вещи: и размер, и способ сборки.
//
// ПОЧЕМУ ЕСТЬ /resolve. Разрешение имени — единственное, что в этом
// сервисе может сломаться от способа линковки, и ровно это ось и меряет.
#include <arpa/inet.h>
#include <netdb.h>
#include <netinet/in.h>
#include <sys/socket.h>
#include <unistd.h>

#include <cstdio>
#include <cstring>
#include <string>

#include <fmt/core.h>

namespace {

// Экранирование для JSON-строк вручную собираемого контракта. Сейчас
// значения (имя варианта, текст ошибки) не содержат кавычек, но проба —
// не единственный источник detail: следующая проба может подставить туда
// произвольный текст (например, эхо запроса), и без экранирования кавычка
// внутри значения молча оборвёт JSON-строку. Такая поломка будет выглядеть
// как сбой пробы, а на деле — дефект сериализации здесь.
std::string json_escape(const std::string& value) {
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
                    out += fmt::format("\\u{:04x}", ch);
                } else {
                    out += static_cast<char>(ch);
                }
        }
    }
    return out;
}

std::string json_line(const char* probe, const char* outcome,
                      const std::string& detail) {
    const char* variant_env = std::getenv("STAND_VARIANT");
    return fmt::format(
        "{{\"kind\":\"package\",\"variant\":\"{}\",\"probe\":\"{}\","
        "\"outcome\":\"{}\",\"detail\":\"{}\"}}",
        json_escape(variant_env ? variant_env : "unknown"),
        probe, outcome, json_escape(detail));
}

// Символьное имя кода getaddrinfo — то, что реально можно сравнивать между
// прогонами. gai_strerror() отдаёт локализуемый текст, который у разных
// libc/musl отличается дословно, а нам нужен код, а не его пересказ.
const char* eai_name(int rc) {
    switch (rc) {
        case EAI_AGAIN: return "EAI_AGAIN";
        case EAI_BADFLAGS: return "EAI_BADFLAGS";
        case EAI_FAIL: return "EAI_FAIL";
        case EAI_FAMILY: return "EAI_FAMILY";
        case EAI_MEMORY: return "EAI_MEMORY";
        case EAI_NONAME: return "EAI_NONAME";
        case EAI_SERVICE: return "EAI_SERVICE";
        case EAI_SOCKTYPE: return "EAI_SOCKTYPE";
        case EAI_SYSTEM: return "EAI_SYSTEM";
#ifdef EAI_OVERFLOW
        case EAI_OVERFLOW: return "EAI_OVERFLOW";
#endif
#ifdef EAI_NODATA
        case EAI_NODATA: return "EAI_NODATA";
#endif
#ifdef EAI_ADDRFAMILY
        case EAI_ADDRFAMILY: return "EAI_ADDRFAMILY";
#endif
        default: return "EAI_UNKNOWN";
    }
}

// ПОЧЕМУ ИСХОД ОСТАЁТСЯ "refused" ДЛЯ ЛЮБОГО КОДА getaddrinfo, А НЕ
// РАЗБИРАЕТСЯ ЗДЕСЬ НА "своя поломка" / "чужой отказ".
//
// Сервис в принципе не может различить их сам. EAI_SYSTEM — это и старый
// glibc в пустом образе (собственно находка этого стенда), и оборванная
// сеть: код один и тот же, а причины разные. Пытаться угадать причину по
// errno или по тексту gai_strerror здесь — значит выдумывать различение,
// которого нет.
//
// Атрибуция — дело контролей на оси, а не разбора кода ошибки: динамическая
// сборка и musl обязаны резолвить в ТОМ ЖЕ прогоне. Если резолвят оба —
// EAI_SYSTEM у третьего варианта это поломка линковки. Если не резолвит ни
// один — это сеть, и дело не в стенде. Поэтому detail несёт символьное имя
// кода (EAI_SYSTEM, EAI_NONAME, ...), а не только текст gai_strerror: разбор
// сравнивает ИМЕНА кодов между вариантами, а не их текстовые описания.
//
// Не "улучшать" эту функцию попыткой различить причины по errno — если
// такое различение станет возможным, это будет новый контроль на оси, а
// не правка resolve().
std::string resolve(const std::string& host) {
    addrinfo hints{};
    hints.ai_family = AF_UNSPEC;
    hints.ai_socktype = SOCK_STREAM;
    addrinfo* res = nullptr;
    const int rc = ::getaddrinfo(host.c_str(), "80", &hints, &res);
    if (rc != 0) {
        const std::string detail =
            fmt::format("{}: {}", eai_name(rc), ::gai_strerror(rc));
        return json_line("resolve", "refused", detail);
    }
    ::freeaddrinfo(res);
    return json_line("resolve", "resolved", "");
}

std::string query_value(const std::string& target, const std::string& key) {
    const auto pos = target.find(key + "=");
    if (pos == std::string::npos) return "";
    const auto start = pos + key.size() + 1;
    const auto end = target.find_first_of("& ", start);
    return target.substr(start, end == std::string::npos ? end : end - start);
}

}  // namespace

int main(int argc, char** argv) {
    int port = 8080;
    for (int i = 1; i < argc; ++i) {
        if (std::strncmp(argv[i], "--port=", 7) == 0) port = std::atoi(argv[i] + 7);
    }

    const int server = ::socket(AF_INET, SOCK_STREAM, 0);
    int yes = 1;
    ::setsockopt(server, SOL_SOCKET, SO_REUSEADDR, &yes, sizeof(yes));
    sockaddr_in addr{};
    addr.sin_family = AF_INET;
    addr.sin_addr.s_addr = INADDR_ANY;
    addr.sin_port = htons(static_cast<uint16_t>(port));
    if (::bind(server, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) != 0) {
        std::fprintf(stderr, "не занять порт %d\n", port);
        return 2;
    }
    ::listen(server, 16);
    // Строка готовности: прогон ждёт её, а не спит по таймеру. Ожидание
    // по таймеру давало бы плавающие прогоны.
    std::fprintf(stderr, "READY %d\n", port);
    std::fflush(stderr);

    for (;;) {
        const int conn = ::accept(server, nullptr, nullptr);
        if (conn < 0) continue;
        char buf[2048] = {};
        const ssize_t n = ::recv(conn, buf, sizeof(buf) - 1, 0);
        std::string body = "ok";
        if (n > 0) {
            const std::string request(buf, static_cast<size_t>(n));
            if (request.find("GET /resolve") == 0) {
                const std::string host = query_value(request, "host");
                body = resolve(host.empty() ? "example.com" : host);
            }
        }
        const std::string response = fmt::format(
            "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n"
            "Content-Length: {}\r\nConnection: close\r\n\r\n{}",
            body.size(), body);
        ::send(conn, response.data(), response.size(), 0);
        ::close(conn);
    }
}
