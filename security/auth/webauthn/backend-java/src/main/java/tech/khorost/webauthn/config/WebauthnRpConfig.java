package tech.khorost.webauthn.config;

import com.webauthn4j.data.client.Origin;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Component;

/**
 * RP-конфигурация (rpId/origin/rpName), читается из env var — единственный
 * механизм конфигурации в проекте (см. CLAUDE.md), без конфиг-файлов с секретами.
 * Имена переменных совпадают с Go-бэкендом (backend-go/main.go), кроме
 * WEBAUTHN_RP_ORIGIN (у Java один origin, не список через запятую).
 */
@Component
public class WebauthnRpConfig {

    private final String rpId;
    private final String rpName;
    private final Origin origin;

    public WebauthnRpConfig(
            @Value("${WEBAUTHN_RP_ID:localhost}") String rpId,
            @Value("${WEBAUTHN_RP_NAME:Khorost WebAuthn Demo (Java)}") String rpName,
            @Value("${WEBAUTHN_RP_ORIGIN:http://localhost:8088}") String rpOrigin) {
        this.rpId = rpId;
        this.rpName = rpName;
        this.origin = new Origin(rpOrigin);
    }

    public String rpId() {
        return rpId;
    }

    public String rpName() {
        return rpName;
    }

    public Origin origin() {
        return origin;
    }
}
