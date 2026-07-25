package tech.khorost.kotlin.bench

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import java.io.IOException
import java.lang.management.ManagementFactory
import java.nio.file.Files
import java.nio.file.Path
import java.util.Locale

/**
 * Тот же сценарий и та же методика измерения, что и в стенде `concurrency/`
 * (Java): N "одновременных" I/O-bound задач, заглушка I/O —
 * `delay(sleepMs)` (аналог Thread.sleep для блокирующих моделей/Mono.delay
 * для Reactor, только неблокирующий на уровне ОС-потока).
 *
 * structured concurrency: `coroutineScope { repeat(n) { launch { ... } } }` —
 * не возвращается, пока не завершатся все N дочерних корутин (аналог
 * CountDownLatch.await() в Java-стенде, но без ручной синхронизации).
 *
 * Диспетчер: `runBlocking` (без аргумента) держит цикл submit на своём
 * собственном потоке, а сами задачи — через `launch(Dispatchers.Default)`
 * (пул потоков размером с число ядер CPU). Так submit-цикл (main thread) не
 * конкурирует с исполнением задач за один и тот же ограниченный пул —
 * эмпирически проверено в этой задаче: вариант "всё через
 * `runBlocking(Dispatchers.Default)`" (submit и исполнение на одном пуле)
 * давал заметно худший p50/p99 (~190мс вместо ~100мс при sleepMs=100), а
 * `Dispatchers.IO` (64 потока) — ещё хуже по throughput, чем `Default` (16
 * потоков) — больше OS-потоков усилило накладные расходы диспетчеризации,
 * а не снизило их. Разделение submit/execute — лучший из трёх проверенных
 * вариантов, но throughput всё равно заметно ниже virtual threads (см.
 * README/отчёт задачи) — не подгонялось под ожидание "корутины ≈ VT".
 *
 * Разрыв throughput с VT — НЕ хвост завершения отдельных задач: `max` во
 * всех прогонах практически равен `p99` (см. README). Прямое измерение
 * (`submit_loop_ms`) показывает, что сам цикл `repeat(n){launch{...}}` —
 * регистрация 10 000 детей одного `coroutineScope` с одного вызывающего
 * потока — занимает 56–75% wall-clock времени прогона. Это и есть основная
 * причина разрыва: стоимость fan-out структурной конкурентности при N=10 000,
 * а не механизм пробуждения по таймеру как таковой (подробности и цифры —
 * README, раздел "Реальный механизм разрыва throughput с VT").
 *
 * java -cp kotlin-backend-0.1.0-all.jar tech.khorost.kotlin.bench.CoroutineBenchKt [n] [sleepMs]
 *
 * Как и в Java-стенде, для честного peak RSS каждый прогон — отдельный
 * процесс JVM (Docker container per run), не переиспользуется между режимами.
 */
fun main(args: Array<String>) {
    val n = args.getOrNull(0)?.toIntOrNull() ?: 10_000
    val sleepMs = args.getOrNull(1)?.toLongOrNull() ?: 100L

    val latenciesNanos = LongArray(n)

    val wallStart = System.nanoTime()
    var submitLoopNanos = 0L
    runBlocking {
        coroutineScope {
            repeat(n) { i ->
                val submitTime = System.nanoTime()
                launch(Dispatchers.Default) {
                    delay(sleepMs)
                    latenciesNanos[i] = System.nanoTime() - submitTime
                }
            }
            // Диагностика: сколько времени сам цикл repeat(n){launch{...}} (10000
            // launch() на одном вызывающем потоке, растущая иерархия детей одного
            // coroutineScope) отнимает от wall-clock ДО implicit-join детей ниже.
            // Печатается здесь же (внутри coroutineScope, сразу после repeat, до
            // того как блок начнёт ждать завершения всех детей).
            submitLoopNanos = System.nanoTime() - wallStart
        }
    }
    val wallNanos = System.nanoTime() - wallStart

    println("submit_loop_ms=" + "%.1f".format(Locale.ROOT, submitLoopNanos / 1_000_000.0))
    printReport("coroutines", n, wallNanos, latenciesNanos)
}

private fun printReport(mode: String, n: Int, wallNanos: Long, latenciesNanos: LongArray) {
    val sorted = latenciesNanos.clone()
    sorted.sort()

    val wallSeconds = wallNanos / 1_000_000_000.0
    val throughput = n / wallSeconds

    val p50Ms = sorted[percentileIndex(sorted.size, 0.50)] / 1_000_000.0
    val p99Ms = sorted[percentileIndex(sorted.size, 0.99)] / 1_000_000.0
    val minMs = sorted[0] / 1_000_000.0
    val maxMs = sorted[sorted.size - 1] / 1_000_000.0

    val peakRssKb = readPeakRssKb()

    val memBean = ManagementFactory.getMemoryMXBean()
    val heapUsedMb = memBean.heapMemoryUsage.used / 1024.0 / 1024.0
    val heapCommittedMb = memBean.heapMemoryUsage.committed / 1024.0 / 1024.0

    println("mode=$mode")
    println("n=$n")
    println("wall_ms=" + "%.1f".format(Locale.ROOT, wallSeconds * 1000))
    println("throughput_tasks_per_sec=" + "%.1f".format(Locale.ROOT, throughput))
    println("latency_p50_ms=" + "%.1f".format(Locale.ROOT, p50Ms))
    println("latency_p99_ms=" + "%.1f".format(Locale.ROOT, p99Ms))
    println("latency_min_ms=" + "%.1f".format(Locale.ROOT, minMs))
    println("latency_max_ms=" + "%.1f".format(Locale.ROOT, maxMs))
    println("peak_rss_kb=$peakRssKb")
    println("heap_used_mb=" + "%.1f".format(Locale.ROOT, heapUsedMb))
    println("heap_committed_mb=" + "%.1f".format(Locale.ROOT, heapCommittedMb))
}

private fun percentileIndex(length: Int, p: Double): Int {
    val idx = Math.ceil(p * length).toInt() - 1
    return idx.coerceIn(0, length - 1)
}

/**
 * Peak RSS процесса из /proc/self/status (VmHWM — историчный пик, не
 * текущее значение) — тот же приём, что и в Java-стенде concurrency/Result.java,
 * для прямой сопоставимости чисел. Linux-only (контейнер).
 */
private fun readPeakRssKb(): Long {
    val status = Path.of("/proc/self/status")
    return try {
        Files.readAllLines(status)
            .firstOrNull { it.startsWith("VmHWM:") }
            ?.trim()
            ?.split(Regex("\\s+"))
            ?.get(1)
            ?.toLong()
            ?: -1L
    } catch (e: IOException) {
        -1L
    }
}
