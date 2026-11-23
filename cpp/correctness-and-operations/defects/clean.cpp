// Ось 1, отрицательный контроль: та же форма — куча, поток, разбор, — без дефекта.
// Ни один детектор не имеет права на ней сработать.
#include <cstdio>
#include <cstdlib>
#include <thread>
#include <atomic>
std::atomic<int> counter{0};
int main(int argc, char**) {
    int* data = static_cast<int*>(std::malloc(4 * sizeof(int)));
    for (int i = 0; i < 4; ++i) data[i] = i * argc;
    int* value = new int(41 + argc);
    std::thread a([] { for (int i = 0; i < 1000; ++i) ++counter; });
    std::thread b([] { for (int i = 0; i < 1000; ++i) ++counter; });
    a.join(); b.join();
    std::printf("%d %d %d\n", data[0] + data[3], *value, counter.load());
    delete value;
    std::free(data);
}
