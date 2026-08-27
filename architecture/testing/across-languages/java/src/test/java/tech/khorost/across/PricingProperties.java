package tech.khorost.across;

import net.jqwik.api.Arbitraries;
import net.jqwik.api.Arbitrary;
import net.jqwik.api.ForAll;
import net.jqwik.api.Property;
import net.jqwik.api.Provide;
import net.jqwik.api.Tag;
import net.jqwik.api.constraints.LongRange;

/**
 * ПРИЁМ 2: property-based на jqwik — тот же сюжет, что в Go (rapid) и Python
 * (Hypothesis): инвариант держится; наивная монотонность находит нарушение
 * НЕНАДЁЖНО; доменный генератор находит стабильно и ужимает до границы тира.
 *
 * <p>Важная деталь для сравнения экосистем: дефолтный бюджет у jqwik — 1000
 * попыток против 100 у rapid и Hypothesis. Поэтому здесь наивное свойство
 * находит нарушение примерно в каждом восьмом прогоне, а в Go и Python на их
 * дефолтах — ни разу за 20. Серию гоняет ../../../../../../property-matrix.sh.
 */
class PricingProperties {

    /** Инвариант: скидка неотрицательна и не больше суммы. Держится. */
    @Property
    void скидкаВРазумныхГраницах(@ForAll @LongRange(min = 0, max = 1_000_000_000L) long total) {
        long d = Pricing.discount(total);
        if (d < 0 || d > total || Pricing.price(total) < 0) {
            throw new AssertionError(
                    "нарушен инвариант: Discount(%d) = %d, Price = %d".formatted(total, d, Pricing.price(total)));
        }
    }

    /** Само свойство: заказ на большую сумму не может стоить дешевле. */
    private static void monotonic(long a, long b) {
        if (a > b) {
            long t = a;
            a = b;
            b = t;
        }
        if (Pricing.price(a) > Pricing.price(b)) {
            throw new AssertionError("заказ на %d стоит %d, а на %d — %d: заплатить больше оказалось дешевле"
                    .formatted(a, Pricing.price(a), b, Pricing.price(b)));
        }
    }

    /**
     * Наивный генератор на бюджете ПО УМОЛЧАНИЮ (у jqwik это 1000 попыток).
     * Демонстрация того, что property-based не магия: нарушение занимает узкую
     * полосу входов, и генератор попадает туда редко — 5 находок на 40 прогонов.
     * Колонка «наивный, бюджет по умолчанию» из README — это он.
     *
     * <p>{@code mvn test -Dgroups=property-demo -Dexcluded.groups=}
     */
    @Property
    @Tag("property-demo")
    void монотонностьНаивныйДефолтныйБюджет(@ForAll @LongRange(min = 0, max = 30_000) long a,
                                            @ForAll @LongRange(min = 0, max = 30_000) long b) {
        monotonic(a, b);
    }

    /**
     * Тот же наивный генератор на 100 попытках — бюджет по умолчанию у rapid и
     * Hypothesis. Нужен для контроля на РАВНОМ бюджете: без него сравнение
     * движков смешивает две переменные — бюджет и алгоритм генерации.
     *
     * <p>{@code mvn test -Dgroups=property-demo -Dexcluded.groups=}
     */
    @Property(tries = 100)
    @Tag("property-demo")
    void монотонностьНаивныйМалыйБюджет(@ForAll @LongRange(min = 0, max = 30_000) long a,
                                        @ForAll @LongRange(min = 0, max = 30_000) long b) {
        monotonic(a, b);
    }

    /**
     * Тот же наивный генератор, но 20 000 попыток — колонка «наивный, 20 000
     * примеров» из README. Отдельный тест, а не правка предыдущего: обе колонки
     * замера должны воспроизводиться без редактирования исходника.
     *
     * <p>{@code mvn test -Dgroups=property-demo -Dexcluded.groups=}
     */
    @Property(tries = 20_000)
    @Tag("property-demo")
    void монотонностьНаивныйБольшойБюджет(@ForAll @LongRange(min = 0, max = 30_000) long a,
                                          @ForAll @LongRange(min = 0, max = 30_000) long b) {
        monotonic(a, b);
    }

    /**
     * Генератор знает СТРУКТУРУ домена (у скидки есть пороги), но не знает про
     * баг: ответ мы не подсказываем, нарушение по-прежнему находит свойство.
     *
     * <p>Запуск: {@code mvn test -Dgroups=property-demo}
     */
    @Property
    @Tag("property-demo")
    void монотонностьДоменныйГенератор(@ForAll("околоТиров") long a, @ForAll("околоТиров") long b) {
        monotonic(a, b);
    }

    @Provide
    Arbitrary<Long> околоТиров() {
        return Arbitraries.oneOf(
                Arbitraries.longs().between(0, 30_000),
                Arbitraries.longs().between(Pricing.TIER1_CENTS - 500, Pricing.TIER1_CENTS + 500),
                Arbitraries.longs().between(Pricing.TIER2_CENTS - 500, Pricing.TIER2_CENTS + 500));
    }
}
