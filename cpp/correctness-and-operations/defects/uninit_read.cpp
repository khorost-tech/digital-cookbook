// Ось 1: чтение неинициализированной переменной.
#include <cstdio>
int main(int argc, char**) {
    int value;
    if (argc > 5) value = 1;                            // при обычном запуске не присвоено
    if (value == 1) std::printf("one\n"); else std::printf("other\n");
}
