import org.jetbrains.kotlin.gradle.dsl.JvmTarget
import org.jetbrains.kotlin.gradle.tasks.KotlinCompile

/*
 * Отдельный Gradle-модуль (Kotlin 2.2.0) — ВНЕ Maven-реактора java-deep-dive/pom.xml.
 * Хостового Gradle нет, сборка только через Docker:
 *
 *   docker run --rm -v "$PWD":/app -v gradle-cache:/home/gradle/.gradle \
 *     -w /app gradle:9-jdk25 gradle build
 *
 * JDK 25 toolchain, НО bytecode target JVM_24: Kotlin 2.2.0 живьём проверен
 * в этой задаче и явно не умеет компилировать под jvmTarget 25 —
 * "Kotlin does not yet support 25 JDK target, falling back to Kotlin JVM_24
 * JVM target" (лог compileKotlin). Компилятор/тесты выполняются на JDK 25
 * (jvmToolchain(25) ниже), но выходной байткод — Java 24. Зафиксировано как
 * факт тулчейна, а не подогнано под ожидание.
 */
plugins {
    kotlin("jvm") version "2.2.0"
    application
    id("com.gradleup.shadow") version "9.5.1"
}

group = "tech.khorost"
version = "0.1.0"

repositories {
    mavenCentral()
}

dependencies {
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-core:1.11.0")

    testImplementation(kotlin("test"))
}

kotlin {
    jvmToolchain(25)
}

tasks.withType<KotlinCompile>().configureEach {
    compilerOptions {
        // Kotlin 2.2.0 не поддерживает JVM_25 как target (см. комментарий выше) —
        // явный JVM_24 вместо неявного fallback, чтобы не ловить предупреждение
        // "Inconsistent JVM Target Compatibility" от compileJava (toolchain=25).
        jvmTarget.set(JvmTarget.JVM_24)
        freeCompilerArgs.add("-Xjsr305=strict")
    }
}

tasks.withType<JavaCompile>().configureEach {
    // Java-исходников в модуле нет, но плагин application/kotlin-jvm заводит
    // compileJava с targetCompatibility от toolchain (25) — выравниваем с
    // Kotlin (24), иначе Gradle 9 валит build с "Inconsistent JVM Target
    // Compatibility Between Java and Kotlin Tasks".
    options.release.set(24)
}

tasks.test {
    useJUnitPlatform()
}

application {
    // По умолчанию запускает идиомы; бенчмарк корутин запускается отдельно
    // через `-PmainClass=...` (см. README) — свой процесс JVM для честного RSS.
    mainClass.set("tech.khorost.kotlin.idioms.IdiomsKt")
}

tasks.shadowJar {
    // Fat-jar без Main-Class в манифесте — режим (idioms vs bench) выбирается
    // явно через `java -cp ... <FQCN>` при запуске (см. README), по аналогии
    // с shaded-jar в `concurrency/`, только там
    // Main-Class фиксирован (один режим на JVM-процесс тоже, но через
    // args[0]). Здесь два разных main() в разных файлах — проще держать без
    // манифеста, чем плодить обёртки.
    archiveClassifier.set("all")
}

// Позволяет запускать конкретный main через:
//   gradle run -PmainClass=tech.khorost.kotlin.bench.CoroutineBenchKt --args="10000 100"
tasks.named<JavaExec>("run") {
    val mainClassProp = project.findProperty("mainClass") as String?
    if (mainClassProp != null) {
        mainClass.set(mainClassProp)
    }
}
