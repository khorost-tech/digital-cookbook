// libparse — разбор ввода, который в C++ ломается тихо: строка запроса
// (`a=1&b=x%20y`) и заголовок PROXY v1 (`PROXY TCP4 src dst sport dport\r\n`).
//
// Библиотека служит двум вещам: примером GoogleTest и CTest для статьи 9 и
// предметом оси 7 (покрытие). Ошибка оси 7 внесена под `#if PLANTED` ВНУТРИ
// функции: оба варианта — один код и один набор символов, различается только
// флаг сборки (lib/planted.hpp).
#pragma once

#include <cstdint>
#include <expected>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

namespace parse {

enum class Error { empty, bad_escape, no_equals, bad_prefix, bad_proto, bad_address, bad_port };

struct Query {
    std::vector<std::pair<std::string, std::string>> params;
};

struct ProxyHeader {
    std::string proto, src, dst;
    std::uint16_t sport = 0, dport = 0;
};

// Строка запроса: пары через '&', ключ и значение через '=', %XX раскодируется.
std::expected<Query, Error> parse_query(std::string_view s);

// Заголовок PROXY v1 из HAProxy: ровно одна строка, CRLF в конце.
std::expected<ProxyHeader, Error> parse_proxy_v1(std::string_view s);

// Порт из десятичной строки: 0..65535.
std::expected<std::uint16_t, Error> parse_port(std::string_view s);

// Допустим ли порт. Отдельная функция ради оси 7 (граница диапазона).
bool valid_port(long v);

// Сколько строк разобрано за время жизни процесса — общий атомарный счётчик.
long parsed_total();

}  // namespace parse
