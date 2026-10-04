package tech.khorost.observability;

import static org.mockito.BDDMockito.given;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.jsonPath;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import java.util.Optional;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.webmvc.test.autoconfigure.WebMvcTest;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.web.servlet.MockMvc;

/**
 * Базовая линия: сервис отвечает верно БЕЗ всякой телеметрии. Если позже, с
 * подключённым агентом, поведение изменится, эти тесты отвечают на вопрос
 * «приложение сломано или инструментирование».
 */
@WebMvcTest(InventoryController.class)
class InventoryControllerTest {

    @Autowired
    private MockMvc mockMvc;

    @MockitoBean
    private InventoryRepository repository;

    @Test
    void returnsItemWhenFound() throws Exception {
        given(repository.findBySku("SKU-0001"))
                .willReturn(Optional.of(new Item("SKU-0001", "Гайка М8", 1000)));

        mockMvc.perform(get("/inventory/SKU-0001"))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.sku").value("SKU-0001"))
                .andExpect(jsonPath("$.quantity").value(1000));
    }

    @Test
    void returns404WhenMissing() throws Exception {
        given(repository.findBySku("NOPE")).willReturn(Optional.empty());

        mockMvc.perform(get("/inventory/NOPE")).andExpect(status().isNotFound());
    }

    @Test
    void failsDeliberatelyForBoomSku() throws Exception {
        mockMvc.perform(get("/inventory/" + InventoryRepository.FAILING_SKU))
                .andExpect(status().isInternalServerError());
    }

    @Test
    void reportsOrdersCount() throws Exception {
        given(repository.countOrders()).willReturn(42L);

        mockMvc.perform(get("/orders/count"))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$").value(42));
    }
}
