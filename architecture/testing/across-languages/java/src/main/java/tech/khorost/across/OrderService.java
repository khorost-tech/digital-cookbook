package tech.khorost.across;

/** Тонкий слой над доменом — то, что тестируем дублёрами. */
public final class OrderService {

    private final Store store;
    private final Notifier notifier;

    public OrderService(Store store, Notifier notifier) {
        this.store = store;
        this.notifier = notifier;
    }

    /**
     * Считает цену, сохраняет заказ, уведомляет пользователя.
     *
     * <p>Системное свойство {@code discount.bug} включает намеренный дефект
     * (скидка теряется) — им проверяем, кто из дублёров его ловит:
     * {@code mvn test -Ddiscount.bug=true}.
     */
    public Order create(String id, String userId, long totalCents) {
        if (totalCents < 0) {
            throw new IllegalArgumentException("сумма заказа отрицательна: " + totalCents);
        }
        long price = Boolean.getBoolean("discount.bug")
                ? totalCents // БАГ: скидка потеряна
                : Pricing.price(totalCents);

        Order o = new Order(id, userId, totalCents, price);
        store.save(o);
        try {
            // Уведомление не критично: заказ уже сохранён.
            notifier.notify(userId, "Заказ " + id + " принят, к оплате " + price + " коп.");
        } catch (RuntimeException ignored) {
            // намеренно проглатываем
        }
        return o;
    }
}
