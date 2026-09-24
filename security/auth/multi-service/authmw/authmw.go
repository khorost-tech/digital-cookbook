// Package authmw — общий HTTP middleware для сервисов-потребителей. Валидирует
// access-токен (JWT), выпущенный auth-сервисом, ЛОКАЛЬНО по общему секрету —
// без похода в auth-сервис на каждый запрос. Единственная связь с auth —
// общий JWT_SECRET.
package authmw

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"khorost.tech/cookbook/auth/internal/jwtclaims"
)

// cookieName — имя cookie с access-токеном, которое выставляет auth-сервис.
const cookieName = "app_at"

type ctxKey int

const claimsCtxKey ctxKey = iota

// Opts настраивает поведение Middleware.
type Opts struct {
	// Optional: если true и токен отсутствует, запрос пропускается дальше без
	// claims в контексте (401 не возвращается). Если токен присутствует, но
	// невалиден — 401 всё равно возвращается.
	Optional bool
}

// Middleware извлекает access-токен из заголовка Authorization: Bearer или,
// если его нет, из cookie app_at, парсит его локально (jwtclaims.Parse) по
// secret и кладёт claims в контекст запроса. При отсутствующем токене и
// !opts.Optional — 401 {"error":"missing_token"}. При невалидном токене —
// 401 {"error":"invalid_token"}.
func Middleware(secret []byte, opts Opts) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := extractToken(r)
			if raw == "" {
				if opts.Optional {
					next.ServeHTTP(w, r)
					return
				}
				writeError(w, http.StatusUnauthorized, "missing_token")
				return
			}

			claims, err := jwtclaims.Parse(secret, raw)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid_token")
				return
			}

			ctx := context.WithValue(r.Context(), claimsCtxKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractToken достаёт токен из заголовка Authorization: Bearer <...>, а при
// его отсутствии — из cookie app_at.
func extractToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		if tok, ok := strings.CutPrefix(auth, "Bearer "); ok {
			return tok
		}
	}
	if c, err := r.Cookie(cookieName); err == nil {
		return c.Value
	}
	return ""
}

// GetClaims достаёт claims, положенные Middleware в контекст запроса. Второй
// возврат — false, если claims в контексте нет (запрос прошёл через
// Opts{Optional: true} без токена).
func GetClaims(ctx context.Context) (*jwtclaims.Claims, bool) {
	c, ok := ctx.Value(claimsCtxKey).(*jwtclaims.Claims)
	return c, ok
}

// RequireRole — middleware поверх Middleware: пропускает запрос, только если
// claims в контексте содержат role. Иначе 403 {"error":"forbidden"}. Должен
// ставиться ПОСЛЕ Middleware в цепочке (Middleware обязан заполнить claims).
func RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := GetClaims(r.Context())
			if !ok || !slices.Contains(claims.Roles, role) {
				writeError(w, http.StatusForbidden, "forbidden")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
