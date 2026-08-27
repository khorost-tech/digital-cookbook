package tech.khorost.across;

/** Сохранённый заказ: totalCent — сумма до скидки, priceCent — к оплате. */
public record Order(String id, String userId, long totalCent, long priceCent) {
}
