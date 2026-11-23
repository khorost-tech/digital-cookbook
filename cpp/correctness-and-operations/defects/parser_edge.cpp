// Ось 1: разбор "ключ=значение"; ошибка срабатывает только при длине ключа 7.
#include <cstdio>
#include <cstring>
bool parse(const char* input, std::size_t size) {
    char key[8];
    const char* eq = static_cast<const char*>(std::memchr(input, '=', size));
    if (eq == nullptr) return false;
    std::size_t length = static_cast<std::size_t>(eq - input);
    if (length == 0 || length > 7) return false;
    std::memcpy(key, input, length);
    key[length + 1] = '\0';                            // при length == 7 — key[8]
    return key[0] != '\0';
}
#ifndef FUZZING
int main() { std::printf("%d\n", parse("name=value", 10)); }
#endif
