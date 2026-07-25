package tech.khorost.kotlin.idioms

/**
 * Extension-функции: добавляют метод к существующему типу без наследования
 * и без изменения его исходников — удобно для тонкого API поверх стандартной
 * библиотеки или сторонних DTO (например, `String`, `List<Article>`).
 */

fun String.toSlug(): String =
    trim()
        .lowercase()
        .replace(Regex("[^a-z0-9\\s-]"), "")
        .replace(Regex("\\s+"), "-")

fun List<Article>.totalViews(): Long = sumOf { it.views }

fun List<Article>.byTag(tag: String): List<Article> = filter { tag in it.tags }

fun extensionsDemo() {
    println("extension-функции:")
    println("  \"WAL и его аналоги #3\".toSlug() = ${"WAL и его аналоги #3".toSlug()}")

    val articles = listOf(
        Article("wal-1", "WAL #1", listOf("databases", "wal"), views = 120),
        Article("wal-2", "WAL #2", listOf("databases", "wal"), views = 80),
        Article("db-indexes-1", "Индексы #1", listOf("databases", "indexes"), views = 200),
    )
    println("  totalViews() = ${articles.totalViews()}")
    println("  byTag(\"wal\") = ${articles.byTag("wal").map { it.slug }}")
}
