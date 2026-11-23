// Ось 1: чтение освобождённой памяти.
#include <cstdio>
int main(int argc, char**) {
    int* value = new int(41 + argc);
    delete value;
    std::printf("%d\n", *value);                        // чтение после delete
}
