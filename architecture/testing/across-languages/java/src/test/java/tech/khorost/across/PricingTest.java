package tech.khorost.across;

import static org.junit.jupiter.api.Assertions.assertEquals;

import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;

/**
 * ПРИЁМ 1: табличный тест. В Go это литерал слайса структур, в Java —
 * @ParameterizedTest с источником данных. Идиома разная, суть одна: примеры
 * выбирает человек.
 */
class PricingTest {

    @ParameterizedTest(name = "{2}: скидка с {0} = {1}")
    @CsvSource({
            "9999,  0,    ниже порога",
            "10000, 500,  ровно 100.00 — 5%",
            "19999, 999,  на копейку ниже 200 — 5% с усечением",
            "20000, 2000, ровно 200.00 — 10%",
            "25000, 2500, 250.00 — 10%",
            "0,     0,    ноль"
    })
    void discount(long total, long want, String name) {
        assertEquals(want, Pricing.discount(total));
    }
}
