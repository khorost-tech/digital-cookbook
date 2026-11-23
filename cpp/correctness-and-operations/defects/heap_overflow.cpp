// Ось 1: запись за конец массива в куче. При i == 4 — за концом блока из 4 int.
#include <cstdio>
#include <cstdlib>
int main(int argc, char**) {
    int* data = static_cast<int*>(std::malloc(4 * sizeof(int)));
    for (int i = 0; i <= 4; ++i) data[i] = i * argc;   // i == 4 — за концом
    std::printf("%d\n", data[0] + data[3]);
    std::free(data);
}
