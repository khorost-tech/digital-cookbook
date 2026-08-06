// Вариант «как было»: свой контейнер Postgres на каждый случай.
//
// Ровно так выглядит тестовый хелпер, написанный самым прямым способом:
// подъём образа, ожидание порта, накат схемы — и всё это столько раз, сколько
// раз тест просит изолированную базу.
package naive_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"tech.khorost/testcontainers-template-db/stand"
)

// freshContainer — контейнер на каждый вызов.
//
// По умолчанию контейнер убирается явно: замер скорости должен сравнивать
// варианты в одинаковых условиях, а не показывать заодно, как один из них
// засоряет демон. Накопление — отдельный сюжет, включается STAND_LEAK=1.
func freshContainer(t *testing.T) string {
	t.Helper()
	ctx := stand.StartCtx(t)

	pg, err := tcpg.Run(ctx, stand.Image,
		tcpg.WithDatabase("app"),
		tcpg.WithUsername("app"), tcpg.WithPassword("app"),
		stand.WithLabels(),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("5432/tcp").WithStartupTimeout(60*time.Second)),
	)
	// Уборка регистрируется ДО проверки ошибки: неудачный Run может оставить
	// частично созданный контейнер, и если выйти по t.Fatalf раньше, он
	// утечёт — тот самый мусор, о котором вся статья. CleanupContainer
	// корректно обрабатывает nil.
	if !stand.LeakContainers() {
		testcontainers.CleanupContainer(t, pg)
	}
	if err != nil {
		t.Fatalf("контейнер не поднялся: %v", err)
	}

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("строка подключения: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, stand.Schema); err != nil {
		t.Fatalf("накат схемы: %v", err)
	}
	return dsn
}

func TestNaive(t *testing.T) {
	stand.SkipIfShort(t)
	for i := range stand.Cases() {
		t.Run(fmt.Sprintf("case-%02d", i), func(t *testing.T) {
			dsn := freshContainer(t)
			assertIsolated(t, dsn)
		})
	}
}

// assertIsolated — проверка, ради которой изоляция и нужна: каждый случай
// пишет одну строку и обязан увидеть ровно одну. Общая база провалила бы её
// на втором же случае.
func assertIsolated(t *testing.T, dsn string) {
	t.Helper()
	ctx := stand.Ctx(t)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx,
		`INSERT INTO accounts (email) VALUES ($1)`, "s@example.test"); err != nil {
		t.Fatalf("вставка: %v", err)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM accounts`).Scan(&n); err != nil {
		t.Fatalf("счёт: %v", err)
	}
	if n != 1 {
		t.Fatalf("база не изолирована: строк %d, ожидалась 1", n)
	}
}
