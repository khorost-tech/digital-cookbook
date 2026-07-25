package tech.khorost.kotlin.idioms

import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.flow
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.take
import kotlinx.coroutines.flow.toList
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull

/**
 * Корутины: `suspend`-функции, structured concurrency (`coroutineScope`,
 * `launch`, `async`/`awaitAll`) и `Flow` (холодный асинхронный поток).
 *
 * Structured concurrency в двух словах: `coroutineScope { ... }` не
 * возвращается, пока не завершатся (успешно или с ошибкой) все дочерние
 * корутины, запущенные внутри блока — не нужен свой CountDownLatch/Future,
 * отмена и ошибки распространяются автоматически по дереву.
 */

suspend fun fetchTitle(slug: String): String {
    delay(20) // имитация сетевого/БД вызова
    return "Заголовок для $slug"
}

suspend fun fetchViews(slug: String): Long {
    delay(15)
    return slug.hashCode().toLong().let { if (it < 0) -it else it } % 1000
}

suspend fun loadArticleCard(slug: String): Article = coroutineScope {
    // async — запускает две suspend-операции параллельно (structured
    // concurrency: обе завершатся или обе отменятся вместе с внешним scope).
    val titleDeferred = async { fetchTitle(slug) }
    val viewsDeferred = async { fetchViews(slug) }
    Article(slug = slug, title = titleDeferred.await(), views = viewsDeferred.await())
}

suspend fun loadManyCards(slugs: List<String>): List<Article> = coroutineScope {
    slugs.map { slug -> async { loadArticleCard(slug) } }.awaitAll()
}

fun articleUpdatesFlow(slug: String, updates: Int): Flow<Long> = flow {
    var views = 0L
    repeat(updates) {
        delay(5)
        views += 10
        emit(views) // холодный поток — код тела запускается заново на каждого collector-а
    }
}

suspend fun coroutinesDemo() = coroutineScope {
    println("корутины (suspend/coroutineScope/async/Flow):")

    val card = loadArticleCard("wal-and-analogs-3")
    println("  loadArticleCard: $card")

    val cards = loadManyCards(listOf("wal-1", "wal-2", "wal-3"))
    println("  loadManyCards (параллельно через async/awaitAll): ${cards.map { it.slug }}")

    // launch (fire-and-forget внутри scope) + структурная отмена по таймауту:
    val timedOut = withTimeoutOrNull(2) {
        launch { delay(50) } // не успеет — родительский withTimeoutOrNull отменит раньше
        delay(50)
        "успел"
    }
    println("  withTimeoutOrNull(2ms) при работе 50ms -> $timedOut (ожидаемо null)")

    val flowResult = articleUpdatesFlow("wal-3", updates = 5).take(3).toList()
    println("  Flow.take(3).toList() = $flowResult")

    val doubled = articleUpdatesFlow("wal-3", updates = 3).map { it * 2 }.toList()
    println("  Flow.map(x2).toList() = $doubled")
}
