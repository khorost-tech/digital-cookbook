#include "parse.hpp"

#include <atomic>

namespace parse {

namespace {

std::atomic<long> g_parsed{0};

int hex(char c) {
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

std::expected<std::string, Error> decode(std::string_view s) {
    std::string out;
    out.reserve(s.size());
    for (std::size_t i = 0; i < s.size(); i++) {
        if (s[i] != '%') { out.push_back(s[i]); continue; }
        if (i + 2 >= s.size()) return std::unexpected(Error::bad_escape);
        int hi = hex(s[i + 1]), lo = hex(s[i + 2]);
        if (hi < 0 || lo < 0) return std::unexpected(Error::bad_escape);
        out.push_back(char(hi * 16 + lo));
        i += 2;
    }
    return out;
}

}  // namespace

std::expected<Query, Error> parse_query(std::string_view s) {
    if (s.empty()) return std::unexpected(Error::empty);
    Query q;
    std::size_t pos = 0;
    while (pos <= s.size()) {
        std::size_t amp = s.find('&', pos);
        if (amp == std::string_view::npos) amp = s.size();
        std::string_view pair = s.substr(pos, amp - pos);
        std::size_t eq = pair.find('=');
        if (eq == std::string_view::npos) return std::unexpected(Error::no_equals);
        auto k = decode(pair.substr(0, eq));
        auto v = decode(pair.substr(eq + 1));
        if (!k || !v) return std::unexpected(Error::bad_escape);
        q.params.emplace_back(std::move(*k), std::move(*v));
        pos = amp + 1;
    }
    g_parsed++;
    return q;
}

std::expected<std::uint16_t, Error> parse_port(std::string_view s) {
    if (s.empty() || s.size() > 20) return std::unexpected(Error::bad_port);
    long v = 0;
    for (char c : s) {
        if (c < '0' || c > '9') return std::unexpected(Error::bad_port);
        v = v * 10 + (c - '0');
        if (v > 70000) return std::unexpected(Error::bad_port);
    }
    if (!valid_port(v)) return std::unexpected(Error::bad_port);
    return static_cast<std::uint16_t>(v);
}

std::expected<ProxyHeader, Error> parse_proxy_v1(std::string_view s) {
    if (!s.starts_with("PROXY ")) return std::unexpected(Error::bad_prefix);
    if (!s.ends_with("\r\n")) return std::unexpected(Error::bad_prefix);
    s.remove_prefix(6);
    s.remove_suffix(2);
    std::string_view f[5];
    for (int i = 0; i < 5; i++) {
        std::size_t sp = s.find(' ');
        if (i < 4 && sp == std::string_view::npos) return std::unexpected(Error::bad_prefix);
        f[i] = s.substr(0, i < 4 ? sp : s.size());
        s.remove_prefix(i < 4 ? sp + 1 : s.size());
    }
    ProxyHeader h;
    if (f[0] != "TCP4" && f[0] != "TCP6") return std::unexpected(Error::bad_proto);
    h.proto = f[0];
    for (int i = 1; i <= 2; i++) {
        if (f[i].empty() || f[i].size() > 39) return std::unexpected(Error::bad_address);
        (i == 1 ? h.src : h.dst) = std::string(f[i]);
    }
    auto sp = parse_port(f[3]);
    auto dp = parse_port(f[4]);
    if (!sp || !dp) return std::unexpected(Error::bad_port);
    h.sport = *sp;
    h.dport = *dp;
    g_parsed++;
    return h;
}

long parsed_total() { return g_parsed; }

}  // namespace parse
