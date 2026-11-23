#include "config.hpp"

#include <charconv>
#include <cstdlib>
#include <string_view>

static std::expected<int, std::string> parse_int(const char* name, int lo, int hi) {
    const char* raw = std::getenv(name);
    if (raw == nullptr) return std::unexpected(std::string(name) + " не задана");
    std::string_view text(raw);
    int value = 0;
    auto [end, error] = std::from_chars(text.data(), text.data() + text.size(), value);
    if (error != std::errc{} || end != text.data() + text.size())
        return std::unexpected(std::string(name) + ": не целое число: «" + raw + "»");
    if (value < lo || value > hi)
        return std::unexpected(std::string(name) + ": вне диапазона");
    return value;
}

std::expected<Config, std::string> read_config_strict() {
    auto port = parse_int("STAND_PORT", 1, 65535);
    if (!port) return std::unexpected(port.error());
    auto timeout = parse_int("STAND_TIMEOUT_MS", 1, 60000);
    if (!timeout) return std::unexpected(timeout.error());
    return Config{*port, *timeout};
}

Config read_config_naive() {
    const char* port = std::getenv("STAND_PORT");
    const char* timeout = std::getenv("STAND_TIMEOUT_MS");
    return Config{port ? std::atoi(port) : 8080, timeout ? std::atoi(timeout) : 1000};
}
