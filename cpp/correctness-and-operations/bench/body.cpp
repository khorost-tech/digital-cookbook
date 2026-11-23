// Ось 2: одно и то же тело бенчмарка в трёх записях и заведомо пустая функция.
// Бенчмарк НЕ запускается на время: считается, осталась ли измеряемая работа в
// собранной функции (scripts/run-bench.py читает дизассемблер). noinline держит
// каждое тело отдельной функцией, чтобы его было что искать по символу.
#include <benchmark/benchmark.h>

__attribute__((noinline)) void body_naive(benchmark::State& state) {
    for (auto _ : state) {
        unsigned sum = 0;
        for (unsigned i = 0; i < 1000; ++i) sum += i * i;
    }
}

__attribute__((noinline)) void body_do_not_optimize(benchmark::State& state) {
    for (auto _ : state) {
        unsigned sum = 0;
        for (unsigned i = 0; i < 1000; ++i) sum += i * i;
        benchmark::DoNotOptimize(sum);
    }
}

__attribute__((noinline)) void body_clobber(benchmark::State& state) {
    static unsigned sink;
    for (auto _ : state) {
        unsigned sum = 0;
        for (unsigned i = 0; i < 1000; ++i) sum += i * i;
        sink = sum;
        benchmark::ClobberMemory();
    }
}

// Не из плана 30.09, добавлена 05.10: граница цикла спрятана от компилятора.
// Разработка признака показала, что три записи выше теряют работу и с
// DoNotOptimize — сумма квадратов при известной границе считается при
// компиляции, а DoNotOptimize защищает результат, не вычисление.
__attribute__((noinline)) void body_opaque_input(benchmark::State& state) {
    unsigned limit = 1000;
    for (auto _ : state) {
        benchmark::DoNotOptimize(limit);
        unsigned sum = 0;
        for (unsigned i = 0; i < limit; ++i) sum += i * i;
        benchmark::DoNotOptimize(sum);
    }
}

__attribute__((noinline)) void body_empty(benchmark::State& state) {
    for (auto _ : state) {}
}

BENCHMARK(body_naive);
BENCHMARK(body_do_not_optimize);
BENCHMARK(body_clobber);
BENCHMARK(body_opaque_input);
BENCHMARK(body_empty);
BENCHMARK_MAIN();
