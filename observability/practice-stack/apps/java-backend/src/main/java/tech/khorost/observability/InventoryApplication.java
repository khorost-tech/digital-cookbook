package tech.khorost.observability;

import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;

/**
 * Java-сервис склада. Никакого кода телеметрии здесь нет и не появится:
 * инструментирование подключается агентом через -javaagent, без правок
 * приложения. Это проверяемое утверждение статьи 1, и проверить его можно
 * только если код действительно чист.
 */
@SpringBootApplication
public class InventoryApplication {

    public static void main(String[] args) {
        SpringApplication.run(InventoryApplication.class, args);
    }
}
