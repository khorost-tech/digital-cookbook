package tech.khorost.kc;

import static org.assertj.core.api.Assertions.assertThat;

import java.util.List;
import java.util.Map;

import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.security.oauth2.jwt.JwtDecoder;
import org.springframework.security.web.SecurityFilterChain;
import org.springframework.test.context.bean.override.mockito.MockitoBean;

/**
 * Проверяет, что контекст поднимается и что realm_access.roles корректно
 * мапятся в authorities ROLE_&lt;role&gt; — как в Go-сервисе.
 * JwtDecoder мокается: реальный делает OIDC discovery по issuer (нужен живой
 * Keycloak), что не нужно для сборки/тестов; в рантайме discovery выполняется
 * на старте (compose поднимает backend-java после healthy keycloak).
 */
@SpringBootTest
class KeycloakDemoApplicationTests {

    @MockitoBean
    JwtDecoder jwtDecoder;

    @Autowired
    SecurityFilterChain securityFilterChain;

    @Test
    void contextLoads() {
        assertThat(securityFilterChain).isNotNull();
    }

    @Test
    void mapsRealmRolesToAuthorities() {
        Jwt jwt = Jwt.withTokenValue("t")
                .header("alg", "RS256")
                .subject("bob-sub")
                .claim("realm_access", Map.of("roles", List.of("user", "admin")))
                .build();

        var authorities = SecurityConfig.extractRealmRoles(jwt);

        assertThat(authorities)
                .extracting("authority")
                .containsExactlyInAnyOrder("ROLE_user", "ROLE_admin");
    }
}
