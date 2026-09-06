package tech.khorost.kc;

import org.springframework.security.oauth2.core.OAuth2Error;
import org.springframework.security.oauth2.core.OAuth2TokenValidator;
import org.springframework.security.oauth2.core.OAuth2TokenValidatorResult;
import org.springframework.security.oauth2.jwt.Jwt;

/**
 * Проверяет, что тип токена — ожидаемый (Bearer у access-токена Keycloak).
 * Отсекает подстановку id_token (typ=ID) и refresh (typ=Refresh) с валидной подписью,
 * issuer, aud и временем: без этой проверки корректно подписанный id_token с aud=backend
 * проходил бы как access-token.
 * <p>
 * ВАЖНО: в Keycloak тип токена лежит в <b>claim</b> {@code typ}, а НЕ в JWT-header typ
 * (там всегда "JWT"). Зеркало Go-сервиса (accessClaims.Typ) — оба проверяют claim typ.
 */
public class TokenTypeValidator implements OAuth2TokenValidator<Jwt> {

    private final String expectedTyp;

    public TokenTypeValidator(String expectedTyp) {
        this.expectedTyp = expectedTyp;
    }

    @Override
    public OAuth2TokenValidatorResult validate(Jwt jwt) {
        Object typ = jwt.getClaim("typ");
        if (expectedTyp.equals(typ)) {
            return OAuth2TokenValidatorResult.success();
        }
        OAuth2Error error = new OAuth2Error(
                "invalid_token",
                "Unexpected token typ, want '" + expectedTyp + "'",
                null);
        return OAuth2TokenValidatorResult.failure(error);
    }
}
