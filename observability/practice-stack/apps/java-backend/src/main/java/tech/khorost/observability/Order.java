package tech.khorost.observability;

import com.fasterxml.jackson.annotation.JsonIgnoreProperties;
import com.fasterxml.jackson.annotation.JsonProperty;

/** Событие, которое публикует go-frontend. Поля совпадают с его OrderResponse. */
@JsonIgnoreProperties(ignoreUnknown = true)
public record Order(
        @JsonProperty("order_id") String orderId,
        String sku,
        int quantity,
        boolean reserved) {
}
