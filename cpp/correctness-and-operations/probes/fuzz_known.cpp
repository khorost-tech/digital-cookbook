// Проба libFuzzer: заведомая находка на входе, начинающемся с "kh!".
// abort(), а не __builtin_trap(): на arm64 trap — это SIGTRAP, который libFuzzer
// не перехватывает, и процесс умирает без отчёта и без файла crash-*
// (замерено первой редакцией этой пробы, код 133).
#include <cstdlib>
#include <cstddef>
#include <cstdint>
extern "C" int LLVMFuzzerTestOneInput(const uint8_t* d, size_t n) {
    if (n >= 3 && d[0] == 'k' && d[1] == 'h' && d[2] == '!') std::abort();
    return 0;
}
