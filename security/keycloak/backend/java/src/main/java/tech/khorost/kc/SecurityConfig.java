package tech.khorost.kc;

import java.util.Collection;
import java.util.List;
import java.util.Map;
import java.util.stream.Collectors;

import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.autoconfigure.security.oauth2.resource.OAuth2ResourceServerProperties;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.security.config.annotation.web.builders.HttpSecurity;
import org.springframework.security.config.annotation.web.configurers.AbstractHttpConfigurer;
import org.springframework.security.core.GrantedAuthority;
import org.springframework.security.core.authority.SimpleGrantedAuthority;
import org.springframework.security.oauth2.core.DelegatingOAuth2TokenValidator;
import org.springframework.security.oauth2.core.OAuth2TokenValidator;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.security.oauth2.jwt.JwtDecoder;
import org.springframework.security.oauth2.jwt.JwtValidators;
import org.springframework.security.oauth2.jwt.NimbusJwtDecoder;
import org.springframework.security.oauth2.server.resource.authentication.JwtAuthenticationConverter;
import org.springframework.security.web.SecurityFilterChain;

/**
 * Конфигурация OAuth2 resource server — зеркало Go-сервиса (backend/internal/auth).
 * <ul>
 *   <li>Локальная проверка подписи по JWKS (discovery по issuer, ленивое получение ключей).</li>
 *   <li>Валидация issuer + exp (по умолчанию), aud=backend ({@link AudienceValidator})
 *       и typ=Bearer ({@link TokenTypeValidator} — отсекает подстановку id_token/refresh).</li>
 *   <li>realm_access.roles → authorities ROLE_&lt;role&gt;.</li>
 *   <li>/public — без токена, /me — любой валидный токен, /admin — роль admin.</li>
 * </ul>
 */
@Configuration
public class SecurityConfig {

    /**
     * JwtDecoder на основе issuer с ленивым получением JWKS (withIssuerLocation),
     * чтобы контекст поднимался без обращения к Keycloak на старте (важно для сборки/тестов).
     * Добавляем валидатор audience поверх дефолтных (issuer + timestamp).
     */
    @Bean
    JwtDecoder jwtDecoder(OAuth2ResourceServerProperties properties,
                          @Value("${kc.audience:backend}") String audience,
                          @Value("${kc.token-type:Bearer}") String tokenType) {
        String issuerUri = properties.getJwt().getIssuerUri();
        NimbusJwtDecoder decoder = NimbusJwtDecoder.withIssuerLocation(issuerUri).build();

        // Дефолтные валидаторы (issuer + timestamp) + aud=backend + typ=Bearer.
        // TokenTypeValidator отсекает подстановку id_token/refresh (тип в claim typ, не header).
        OAuth2TokenValidator<Jwt> withIssuer = JwtValidators.createDefaultWithIssuer(issuerUri);
        OAuth2TokenValidator<Jwt> validators = new DelegatingOAuth2TokenValidator<>(
                withIssuer,
                new AudienceValidator(audience),
                new TokenTypeValidator(tokenType));
        decoder.setJwtValidator(validators);
        return decoder;
    }

    /** Достаёт роли из realm_access.roles и мапит в authorities ROLE_&lt;role&gt;. */
    @Bean
    JwtAuthenticationConverter jwtAuthenticationConverter() {
        JwtAuthenticationConverter converter = new JwtAuthenticationConverter();
        converter.setJwtGrantedAuthoritiesConverter(SecurityConfig::extractRealmRoles);
        return converter;
    }

    @SuppressWarnings("unchecked")
    static Collection<GrantedAuthority> extractRealmRoles(Jwt jwt) {
        Object realmAccess = jwt.getClaim("realm_access");
        if (!(realmAccess instanceof Map<?, ?> map)) {
            return List.of();
        }
        Object roles = map.get("roles");
        if (!(roles instanceof Collection<?> roleList)) {
            return List.of();
        }
        return roleList.stream()
                .map(Object::toString)
                .map(role -> new SimpleGrantedAuthority("ROLE_" + role))
                .collect(Collectors.toList());
    }

    @Bean
    SecurityFilterChain securityFilterChain(HttpSecurity http,
                                            JwtAuthenticationConverter jwtAuthenticationConverter) throws Exception {
        http
                .csrf(AbstractHttpConfigurer::disable)
                .authorizeHttpRequests(auth -> auth
                        .requestMatchers("/public").permitAll()
                        .requestMatchers("/actuator/health", "/actuator/health/**").permitAll()
                        .requestMatchers("/admin").hasRole("admin")
                        .requestMatchers("/me").authenticated()
                        .anyRequest().authenticated())
                .oauth2ResourceServer(oauth2 -> oauth2
                        .jwt(jwt -> jwt.jwtAuthenticationConverter(jwtAuthenticationConverter)));
        return http.build();
    }
}
