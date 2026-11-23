// Ось 1: гонка двух потоков на общей переменной без синхронизации.
#include <cstdio>
#include <thread>
int counter = 0;
int main() {
    std::thread a([] { for (int i = 0; i < 100000; ++i) ++counter; });
    std::thread b([] { for (int i = 0; i < 100000; ++i) ++counter; });
    a.join(); b.join();
    std::printf("%d\n", counter);
}
