package tech.khorost.across;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;

import java.util.ArrayList;
import java.util.List;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;

/**
 * ПРИЁМ 3: дублёры. Java-специфика: здесь есть Mockito, и мок пишется в одну
 * строку — поэтому в Java соблазн мокать всё сильнее, чем в Go, где дублёра
 * надо писать руками.
 *
 * <p>Проверить контраст: {@code mvn test -Ddiscount.bug=true} — падают тест на
 * фейке И тест на моке с захватом аргумента; зелёной остаётся только слабая
 * проверка числа вызовов. Пропускает не мок, а слабое утверждение.
 */
class DoublesTest {

    /** ФЕЙК: рабочая реализация в памяти. */
    static final class FakeStore implements Store {
        private final List<Order> orders = new ArrayList<>();

        @Override
        public void save(Order o) {
            orders.add(o);
        }

        @Override
        public List<Order> byUser(String userId) {
            return orders.stream().filter(o -> o.userId().equals(userId)).toList();
        }
    }

    @Test
    void наФейке_заказСохранёнСоСкидкой() {
        FakeStore store = new FakeStore();
        OrderService svc = new OrderService(store, mock(Notifier.class));

        svc.create("o-1", "u-1", 10_000);

        List<Order> got = store.byUser("u-1");
        assertEquals(1, got.size(), "ожидали 1 заказ");
        assertEquals(9_500, got.get(0).priceCent(), "к оплате: 100.00 минус 5%");
    }

    /**
     * СЛАБАЯ проверка вызовов: хранилище позвали ровно один раз. Утверждение
     * осмысленное, но про цену не знает — под -Ddiscount.bug=true остаётся
     * зелёным, хотя клиент платит полную сумму.
     */
    @Test
    void наМоке_слабаяПроверка_толькоЧислоВызовов() {
        Store store = mock(Store.class);
        OrderService svc = new OrderService(store, mock(Notifier.class));

        svc.create("o-1", "u-1", 10_000);

        verify(store, times(1)).save(any(Order.class));
    }

    /**
     * СИЛЬНАЯ проверка вызовов: ArgumentCaptor достаёт то, что реально понесли
     * в хранилище. Под -Ddiscount.bug=true падает — как и тест на фейке.
     *
     * <p>Отсюда честный вывод: дело не в «фейк умеет, мок не умеет», а в том,
     * ЧТО утверждает тест. Разница между ними — в предмете: фейк говорит про
     * результат, мок — про вызов.
     */
    @Test
    void наМоке_сильнаяПроверка_захватАргумента() {
        Store store = mock(Store.class);
        OrderService svc = new OrderService(store, mock(Notifier.class));

        svc.create("o-1", "u-1", 10_000);

        ArgumentCaptor<Order> captor = ArgumentCaptor.forClass(Order.class);
        verify(store).save(captor.capture());
        assertEquals(9_500, captor.getValue().priceCent(),
                "в хранилище понесли цену: 100.00 минус 5%");
    }

    /** СТАБ: ошибка уведомления не роняет заказ. */
    @Test
    void наСтабе_ошибкаУведомленияНеРоняетЗаказ() {
        FakeStore store = new FakeStore();
        Notifier notifier = mock(Notifier.class);
        doThrow(new RuntimeException("канал недоступен")).when(notifier).notify(anyString(), anyString());

        OrderService svc = new OrderService(store, notifier);

        assertDoesNotThrow(() -> svc.create("o-1", "u-1", 5_000));
        assertEquals(1, store.byUser("u-1").size(), "заказ должен быть сохранён");
    }
}
