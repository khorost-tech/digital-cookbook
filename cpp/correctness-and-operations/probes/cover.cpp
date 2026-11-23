// Проба llvm-cov: строки, ветви и MC/DC у clang 19.
#include <cstdio>
#include <cstdlib>
int f(int a, int b) { if (a > 0 && b > 0) return 1; return 0; }
int main(int argc, char**) { std::printf("%d\n", f(argc, 1)); }
