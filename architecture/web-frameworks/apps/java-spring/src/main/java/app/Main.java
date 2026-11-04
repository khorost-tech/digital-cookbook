// Полный: Spring Boot — автоконфигурация, контейнер внедрения, MVC-контроллеры, обработка ошибок по умолчанию.
// Валидация тела (@Valid) — отдельный стартер spring-boot-starter-validation.
package app;

import java.util.Map;
import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.http.HttpStatus;
import org.springframework.web.bind.annotation.*;

@SpringBootApplication
@RestController
public class Main {
    public record Item(String name, int qty) {}

    @GetMapping("/items/{id}")
    Map<String, Object> get(@PathVariable int id) { return Map.of("id", id, "name", "item"); }

    @PostMapping("/items")
    @ResponseStatus(HttpStatus.CREATED)
    Item create(@RequestBody Item item) { return item; }

    @GetMapping("/boom")
    String boom() { throw new IllegalStateException("boom: секрет в сообщении marker-7f3a9c"); }

    public static void main(String[] args) { SpringApplication.run(Main.class, args); }
}
