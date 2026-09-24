package tech.khorost.webauthn;

import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;

/**
 * Demo WebAuthn relying party — Java-контраст к Go-бэкенду (backend-go): те же
 * четыре endpoint'а (/register/begin|finish, /login/begin|finish), тот же контракт
 * JSON, верификация через webauthn4j вместо go-webauthn.
 */
@SpringBootApplication
public class WebauthnApplication {

    public static void main(String[] args) {
        SpringApplication.run(WebauthnApplication.class, args);
    }
}
