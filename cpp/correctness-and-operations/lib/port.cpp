// valid_port — отдельным файлом ради оси 7: сводка llvm-cov по файлу и есть
// сводка по этой функции (строки, ветви, MC/DC), без выделения функции из
// общего отчёта.
#include "parse.hpp"
#include "planted.hpp"

namespace parse {

bool valid_port(long v) {
#if PLANTED
    return v >= 0 && v <= 65536;                       // P1
#else
    return v >= 0 && v <= 65535;
#endif
}

}  // namespace parse
