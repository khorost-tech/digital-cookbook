package tech.khorost.kc;

import java.util.List;
import java.util.Map;

import org.springframework.security.core.annotation.AuthenticationPrincipal;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;

/**
 * Эндпоинты, зеркалящие Go-сервис (backend/go/main.go):
 * <ul>
 *   <li>{@code /public} — 200 без токена.</li>
 *   <li>{@code /me} — sub + roles (как Go: {@code {"sub":..., "roles":[...]}}).</li>
 *   <li>{@code /admin} — требует роль admin, отдаёт message + sub + roles.</li>
 * </ul>
 */
@RestController
public class ApiController {

    @GetMapping("/public")
    public Map<String, String> publicEndpoint() {
        return Map.of("message", "public endpoint, no token required");
    }

    @GetMapping("/me")
    public Map<String, Object> me(@AuthenticationPrincipal Jwt jwt) {
        return Map.of(
                "sub", jwt.getSubject(),
                "roles", realmRoles(jwt));
    }

    @GetMapping("/admin")
    public Map<String, Object> admin(@AuthenticationPrincipal Jwt jwt) {
        return Map.of(
                "message", "admin endpoint",
                "sub", jwt.getSubject(),
                "roles", realmRoles(jwt));
    }

    private static List<String> realmRoles(Jwt jwt) {
        Object realmAccess = jwt.getClaim("realm_access");
        if (realmAccess instanceof Map<?, ?> map && map.get("roles") instanceof List<?> roles) {
            return roles.stream().map(Object::toString).toList();
        }
        return List.of();
    }
}
