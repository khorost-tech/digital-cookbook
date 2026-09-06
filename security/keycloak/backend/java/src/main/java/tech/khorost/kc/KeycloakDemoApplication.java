package tech.khorost.kc;

import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;

/**
 * Точка входа Spring Boot resource server — зеркало Go-сервиса (backend/go).
 * Валидирует access-токены Keycloak по JWKS и разграничивает доступ по realm-ролям.
 */
@SpringBootApplication
public class KeycloakDemoApplication {
    public static void main(String[] args) {
        SpringApplication.run(KeycloakDemoApplication.class, args);
    }
}
