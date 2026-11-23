// Вход libFuzzer для parser_edge.cpp (собирается с -DFUZZING).
#include <cstddef>
#include <cstdint>
bool parse(const char* input, std::size_t size);
extern "C" int LLVMFuzzerTestOneInput(const std::uint8_t* data, std::size_t size) {
    parse(reinterpret_cast<const char*>(data), size);
    return 0;
}
