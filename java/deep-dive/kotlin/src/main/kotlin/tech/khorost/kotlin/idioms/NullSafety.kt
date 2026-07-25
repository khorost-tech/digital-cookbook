package tech.khorost.kotlin.idioms

/**
 * Null-safety: `?` (nullable-тип), безопасный вызов `?.`, elvis `?:`,
 * non-null assertion `!!` (и почему его почти никогда не стоит использовать
 * в бэкенд-коде — предпочтителен явный `?:` с осмысленным дефолтом/ошибкой).
 */

data class UserProfile(val id: Long, val displayName: String?, val email: String)

fun greet(profile: UserProfile?): String {
    // Безопасный вызов + elvis: если profile == null или displayName == null,
    // подставляем дефолт. Не бросает NPE ни на одном шаге цепочки.
    val name = profile?.displayName ?: "аноним"
    return "Привет, $name!"
}

fun requireEmail(profile: UserProfile): String {
    // Здесь email не nullable по типу — компилятор гарантирует, что до этой
    // точки значение уже проверено на этапе создания UserProfile.
    return profile.email
}

fun unsafeDemo(profile: UserProfile?): Int {
    // !! — намеренно провоцирует NPE, если profile == null. Оставлено как
    // иллюстрация антипаттерна: в проде почти всегда лучше explicit-check
    // или ?: с логированием/ошибкой, а не "положиться и забыть".
    return profile!!.displayName!!.length
}

fun nullSafetyDemo() {
    val known = UserProfile(1, "Александр", "a@khorost.tech")
    val anonymous = UserProfile(2, null, "anon@khorost.tech")

    println("null-safety:")
    println("  ${greet(known)}")
    println("  ${greet(anonymous)}")
    println("  ${greet(null)}")
    println("  requireEmail(known) = ${requireEmail(known)}")
    runCatching { unsafeDemo(anonymous) }
        .onFailure { println("  unsafeDemo(anonymous) упал ожидаемо: ${it::class.simpleName}") }
}
