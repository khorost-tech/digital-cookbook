package tech.khorost.kotlin.idioms

/**
 * Sealed classes + when: типобезопасное представление конечного набора
 * вариантов результата (аналог Result-типов без исключений на каждый чих).
 * Компилятор проверяет исчерпываемость `when` — забытая ветка не
 * компилируется, если убрать `else`.
 */
sealed interface PublishResult {
    data class Published(val slug: String, val url: String) : PublishResult
    data class ScheduledFor(val slug: String, val whenUtc: String) : PublishResult
    data class Rejected(val slug: String, val reason: String) : PublishResult
}

fun describe(result: PublishResult): String = when (result) {
    is PublishResult.Published -> "опубликовано: ${result.url}"
    is PublishResult.ScheduledFor -> "запланировано на ${result.whenUtc}"
    is PublishResult.Rejected -> "отклонено: ${result.reason}"
    // без else — компилятор сам проверяет, что все варианты sealed-иерархии покрыты
}

fun sealedClassesDemo() {
    val results = listOf(
        PublishResult.Published("wal-3", "https://khorost.tech/databases/wal-3/"),
        PublishResult.ScheduledFor("db-indexes-4", "2026-07-22T07:00:00+03:00"),
        PublishResult.Rejected("draft-x", "нет обложки"),
    )
    println("sealed classes + when:")
    results.forEach { println("  ${describe(it)}") }
}
