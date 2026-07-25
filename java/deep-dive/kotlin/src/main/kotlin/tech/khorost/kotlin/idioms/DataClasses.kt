package tech.khorost.kotlin.idioms

/**
 * Data classes: equals/hashCode/toString/copy генерируются компилятором.
 * Типичный бэкенд-кейс — DTO/value-объект для API-ответа или строки из БД.
 */
data class Article(
    val slug: String,
    val title: String,
    val tags: List<String> = emptyList(),
    val views: Long = 0,
)

fun dataClassesDemo() {
    val article = Article("wal-and-analogs-1", "WAL и его аналоги", listOf("databases", "wal"))
    val withMoreViews = article.copy(views = article.views + 1) // copy — иммутабельное обновление одного поля

    println("data classes:")
    println("  $article") // авто-toString: Article(slug=..., title=..., tags=..., views=...)
    println("  equals: ${article == article.copy()}") // структурное равенство, не ссылочное
    println("  copy с инкрементом views: $withMoreViews")

    val (slug, title) = article // destructuring по componentN(), сгенерированным для data class
    println("  destructuring: slug=$slug, title=$title")
}
