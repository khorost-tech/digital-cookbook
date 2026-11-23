// Ось 4: конфигурация из env двумя способами — наивно (atoi) и строго
// (from_chars, результат в std::expected, отказ с причиной на старте).
#pragma once

#include <expected>
#include <string>

struct Config {
    int port;
    int timeout_ms;
};

Config read_config_naive();
std::expected<Config, std::string> read_config_strict();
