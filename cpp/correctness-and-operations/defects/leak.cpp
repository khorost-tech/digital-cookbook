// Ось 1: потерянная память — delete[] нет.
#include <cstdio>
int main(int argc, char**) {
    int* lost = new int[16];
    lost[0] = argc;
    std::printf("%d\n", lost[0]);                       // delete[] нет
}
