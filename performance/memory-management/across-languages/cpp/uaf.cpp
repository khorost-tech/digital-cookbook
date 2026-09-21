// uaf.cpp
//
// Опора 2: характерный use-after-free ловится САНИТАЙЗЕРОМ в рантайме,
// в отличие от Rust, где ЭТОТ ЖЕ паттерн не компилируется (borrow checker,
// ошибка E0502 — см. rust/borrow_error/uaf.rs).
//
// Паттерн ровно как в Rust-примере: держим указатель на элемент вектора,
// затем push_back реаллоцирует буфер (старый освобождается), и мы читаем
// висячий указатель. В Rust это ошибка компиляции; в C++ компилируется
// молча и ловится только ASan в рантайме ("heap-use-after-free").

#include <cstdio>
#include <vector>

int main() {
    std::vector<int> v = {1, 2, 3};
    const int* first = &v[0];   // указатель на первый элемент (как &v[0] в Rust)

    std::printf("before push: *first = %d, size=%zu, cap=%zu\n",
                *first, v.size(), v.capacity());
    std::fflush(stdout);        // ASan прервёт процесс на UAF ниже -> сбрасываем буфер

    v.push_back(4);             // размер > ёмкости -> реаллокация: старый буфер освобождён,
                                // first теперь висячий

    // Use-after-free: читаем освобождённый старый буфер.
    std::printf("after push:  *first = %d\n", *first);
    return 0;
}
