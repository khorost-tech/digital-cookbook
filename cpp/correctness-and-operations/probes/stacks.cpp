// Проба стеков perf: leaf() вызывается из mid(), mid() — из main().
// noinline держит функции отдельными на -O2, иначе стек схлопнется по другой причине.
#include <cmath>
#include <cstdio>
__attribute__((noinline)) double leaf(int n) { double s = 0; for (int i = 1; i < n; i++) s += std::sqrt(double(i)); return s; }
__attribute__((noinline)) double mid(int n) { return leaf(n) + 1; }
int main() { double s = 0; for (int k = 0; k < 300; k++) s += mid(1000000); std::printf("%f\n", s); }
