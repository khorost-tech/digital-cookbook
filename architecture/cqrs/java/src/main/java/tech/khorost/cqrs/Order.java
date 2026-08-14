package tech.khorost.cqrs;

/**
 * Order — денормализованное представление заказа (то, что видит read-сторона).
 * <p>
 * updatedSeq — позиция (seq) события, до которого «свёрнут» этот заказ; служит и
 * токеном read-your-writes: «моя запись видна, когда проектор дошёл до этого seq».
 */
public record Order(long orderId, String userId, String status, long amount, long updatedSeq) {

    /** Строковая форма для логов: {@code #1002(new,300)} — как orderIDs в Go-стенде. */
    public String tag() {
        return "#" + orderId + "(" + status + "," + amount + ")";
    }
}
