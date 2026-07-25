package tech.khorost.springvsquarkus.spring;

import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;

/**
 * Spring Boot сторона сравнения spring-vs-quarkus: минимальный REST-сервис,
 * эквивалентный quarkus/HelloResource.java. GET /hello -> "hello".
 * GET /health отдаёт actuator (см. application.properties, base-path=/).
 */
@SpringBootApplication
@RestController
public class App {

    @GetMapping("/hello")
    public String hello() {
        return "hello";
    }

    public static void main(String[] args) {
        SpringApplication.run(App.class, args);
    }
}
