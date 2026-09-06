package tech.khorost.kc;

import static org.assertj.core.api.Assertions.assertThat;

import java.util.List;
import java.util.Map;

import org.junit.jupiter.api.Test;
import org.springframework.security.oauth2.jwt.Jwt;

/**
 * Негативные тесты типа токена: access-token (typ=Bearer) проходит, а подстановка
 * id_token (typ=ID) и refresh (typ=Refresh) — отклоняется. Зеркало Go-тестов.
 */
class TokenTypeValidatorTest {

    private final TokenTypeValidator validator = new TokenTypeValidator("Bearer");

    private static Jwt jwtWithTyp(String typ) {
        Jwt.Builder b = Jwt.withTokenValue("t")
                .header("alg", "RS256")
                .subject("bob-sub")
                .claim("realm_access", Map.of("roles", List.of("user")));
        if (typ != null) {
            b = b.claim("typ", typ);
        }
        return b.build();
    }

    @Test
    void acceptsBearerAccessToken() {
        assertThat(validator.validate(jwtWithTyp("Bearer")).hasErrors()).isFalse();
    }

    @Test
    void rejectsIdToken() {
        assertThat(validator.validate(jwtWithTyp("ID")).hasErrors()).isTrue();
    }

    @Test
    void rejectsRefreshToken() {
        assertThat(validator.validate(jwtWithTyp("Refresh")).hasErrors()).isTrue();
    }

    @Test
    void rejectsMissingTyp() {
        assertThat(validator.validate(jwtWithTyp(null)).hasErrors()).isTrue();
    }
}
