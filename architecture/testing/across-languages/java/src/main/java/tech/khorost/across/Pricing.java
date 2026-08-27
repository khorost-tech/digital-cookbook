package tech.khorost.across;

/**
 * Доменное ядро — тот же код, что в go/pricing и python/pricing.
 * Объёмная скидка по тирам, всё в копейках.
 */
public final class Pricing {

    /** От 100.00 — 5%. */
    public static final long TIER1_CENTS = 10_000;
    /** От 200.00 — 10%. */
    public static final long TIER2_CENTS = 20_000;

    private Pricing() {
    }

    /** Размер скидки в копейках для суммы заказа. */
    public static long discount(long totalCents) {
        if (totalCents >= TIER2_CENTS) {
            return totalCents * 10 / 100;
        }
        if (totalCents >= TIER1_CENTS) {
            return totalCents * 5 / 100;
        }
        return 0;
    }

    /** Итоговая цена: сумма минус скидка. */
    public static long price(long totalCents) {
        return totalCents - discount(totalCents);
    }
}
