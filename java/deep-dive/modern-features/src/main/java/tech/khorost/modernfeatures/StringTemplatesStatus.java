package tech.khorost.modernfeatures;

/**
 * String templates — статус на JDK 25: REMOVED (не показываем как рабочий код).
 *
 * <p>История фичи:
 * <ul>
 *   <li>JDK 21 — preview, JEP 430 "String Templates (Preview)": синтаксис
 *       {@code STR."Hello \{name}"}, {@code FMT."%3d\{n}"}.</li>
 *   <li>JDK 22 — второй preview, JEP 459 "String Templates (Second Preview)":
 *       та же идея, доработки API (StringTemplate.Processor и т.д.).</li>
 *   <li>JDK 23 — фича СНЯТА с релиза целиком, на редизайн (OpenJDK решил, что
 *       API процессоров нуждается в переработке — не тот случай, когда preview
 *       просто "не финализировали", а именно withdrawn из feature set).</li>
 *   <li>JDK 24, JDK 25 — синтаксис STR./FMT."..." по-прежнему ОТСУТСТВУЕТ.
 *       Фактически проверено на {@code maven:3.9-eclipse-temurin-25} (javac 25.0.3):
 *       скормили компилятору {@code javac --release 25} строку
 *       {@code String s = STR."Hello \{name}";} (в отдельном скретч-файле вне
 *       этого модуля — модуль им не собирается). Компилятор НЕ распознаёт
 *       {@code STR."..."} как шаблонный процессор вообще — он просто видит
 *       обычный строковый литерал и падает на {@code \{} внутри него:
 *       {@code error: illegal escape character}. То есть на 25 нет даже
 *       частичного разбора отменённого синтаксиса, {@code \{} трактуется как
 *       невалидный escape-символ, как и в любой обычной Java-строке.</li>
 * </ul>
 *
 * <p>Рабочие альтернативы на JDK 25 (никакого preview-флага не нужно):
 * {@link String#format(String, Object...)}, {@link String#formatted(Object...)},
 * обычная конкатенация {@code +}. Ниже — три сниппета, которые реально
 * компилируются и делают то же самое, что задумывался STR-процессор.
 */
public final class StringTemplatesStatus {

    public static void main(String[] args) {
        String name = "Хорост";
        int articlesCount = 42;

        // Альтернатива 1: String.format — самый близкий по духу аналог %-плейсхолдеров.
        String viaFormat = String.format("Привет, %s! Статей опубликовано: %d.", name, articlesCount);
        System.out.println(viaFormat);

        // Альтернатива 2: String.formatted — тот же формат, но вызов "от строки"
        // (появился в JDK 15, JEP 378 text blocks context, финализирован задолго до 25).
        String viaFormatted = "Привет, %s! Статей опубликовано: %d.".formatted(name, articlesCount);
        System.out.println(viaFormatted);

        // Альтернатива 3: обычная конкатенация — без форматирования чисел/паддинга,
        // но проще всего для коротких строк.
        String viaConcat = "Привет, " + name + "! Статей опубликовано: " + articlesCount + ".";
        System.out.println(viaConcat);

        // Text blocks (JEP 378, finalized в JDK 15 — НЕ связаны со string templates
        // напрямую, но часто путают) по-прежнему работают на 25 и комбинируются
        // с formatted() для многострочных шаблонов:
        String multiline = """
                Автор: %s
                Статей: %d
                """.formatted(name, articlesCount);
        System.out.print(multiline);
    }
}
