package tech.khorost.kotlin.idioms

import kotlinx.coroutines.runBlocking

/**
 * Единая точка входа: прогоняет все идиомы подряд (null-safety, data classes,
 * extension-функции, sealed classes/when, корутины). Каждый блок — независимая
 * демонстрация с println, без внешних зависимостей (БД/сеть) — читается и
 * гоняется как есть.
 *
 * gradle run                                    # эта точка входа
 * gradle run -PmainClass=tech.khorost.kotlin.bench.CoroutineBenchKt --args="10000 100"
 */
fun main() {
    nullSafetyDemo()
    println()
    dataClassesDemo()
    println()
    extensionsDemo()
    println()
    sealedClassesDemo()
    println()
    runBlocking { coroutinesDemo() }
}
