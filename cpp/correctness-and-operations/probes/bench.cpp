// Проба Google Benchmark: собирается и печатает JSON.
#include <benchmark/benchmark.h>
static void BM_Sum(benchmark::State& s) { for (auto _ : s) { int x = 0; for (int i = 0; i < 100; i++) x += i; benchmark::DoNotOptimize(x); } }
BENCHMARK(BM_Sum);
BENCHMARK_MAIN();
