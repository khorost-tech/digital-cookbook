// Вариант «как стало»: один контейнер на тестовый бинарь, база-шаблон со
// схемой, и CREATE DATABASE ... TEMPLATE на каждый случай.
//
// Изоляция та же, что у варианта naive — проверяется тем же assertIsolated.
// Отличается только цена: копирование файлов внутри одного сервера вместо
// нового контейнера.
//
// Уборка здесь ЯВНАЯ, через TestMain. sync.Once оставлен для ленивого подъёма
// (не нужно трогать вызовы), но полагаться на реапер как на способ уборки не
// стоит: он аварийный запасной путь, срабатывает с задержкой и отключается
// переменной TESTCONTAINERS_RYUK_DISABLED, которую в CI ставят чаще, чем
// кажется.
package tmpl_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"tech.khorost/testcontainers-template-db/stand"
)

const (
	adminUser = "app"
	adminPass = "app"
	// adminDB — служебная база для CREATE/DROP DATABASE. Она НЕ может быть
	// шаблоном: Postgres запрещает использовать базу как TEMPLATE, пока к ней
	// есть соединения.
	adminDB    = "postgres"
	templateDB = "stand_template"
)

var (
	once      sync.Once
	setupErr  error
	pool      *pgxpool.Pool
	endpoint  string
	container testcontainers.Container
)

// TestMain — явная уборка после последнего теста пакета. Без неё контейнер
// живёт до реапера: тот убирает его сам, но с задержкой и только если смог
// стартовать.
func TestMain(m *testing.M) {
	code := m.Run()
	if pool != nil {
		pool.Close()
	}
	if container != nil {
		ctx, cancel := context.WithTimeout(context.Background(), stand.Timeout)
		if err := testcontainers.TerminateContainer(container, testcontainers.StopContext(ctx)); err != nil {
			fmt.Fprintf(os.Stderr, "стенд: контейнер не убран: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
		cancel()
	}
	os.Exit(code)
}

func setup(ctx context.Context) error {
	pg, err := tcpg.Run(ctx, stand.Image,
		tcpg.WithDatabase(adminDB),
		tcpg.WithUsername(adminUser), tcpg.WithPassword(adminPass),
		stand.WithLabels(),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("5432/tcp").WithStartupTimeout(60*time.Second)),
	)
	// Присваиваем ДО проверки ошибки: неудачный Run может вернуть частично
	// созданный контейнер, и без этого TestMain о нём не узнает.
	container = pg
	if err != nil {
		return fmt.Errorf("контейнер: %w", err)
	}

	ep, err := pg.PortEndpoint(ctx, "5432/tcp", "")
	if err != nil {
		return fmt.Errorf("порт: %w", err)
	}
	endpoint = ep

	pool, err = pgxpool.New(ctx, dsnFor(adminDB))
	if err != nil {
		return fmt.Errorf("соединение к служебной базе: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}

	if _, err := pool.Exec(ctx, "CREATE DATABASE "+templateDB); err != nil {
		return fmt.Errorf("создание шаблона: %w", err)
	}

	// Схема катится в шаблон ОДИН раз. Соединение к шаблону закрывается сразу
	// же: пока оно открыто, CREATE DATABASE ... TEMPLATE отобьётся с
	// «source database is being accessed by other users».
	tp, err := pgxpool.New(ctx, dsnFor(templateDB))
	if err != nil {
		return fmt.Errorf("соединение к шаблону: %w", err)
	}
	_, err = tp.Exec(ctx, stand.Schema)
	tp.Close()
	if err != nil {
		return fmt.Errorf("накат схемы в шаблон: %w", err)
	}
	return nil
}

func dsnFor(db string) string {
	return fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable", adminUser, adminPass, endpoint, db)
}

// newDB — свежая база из шаблона. Имя случайное: t.Name() не годится,
// подтесты содержат «/» и прочие символы, недопустимые в непокавыченном
// идентификаторе Postgres.
func newDB(t *testing.T) string {
	t.Helper()

	// Подъём общего контейнера получает СВОЙ бюджет, а не бюджет теста,
	// которому не повезло оказаться первым: контейнер живёт дольше любого
	// теста, и 30-секундного дедлайна на операции с базой ему не хватит.
	once.Do(func() {
		setupCtx, cancel := context.WithTimeout(context.Background(), stand.StartupTimeout)
		defer cancel()
		setupErr = setup(setupCtx)
	})
	if setupErr != nil {
		t.Fatalf("общий контейнер-шаблон не поднялся: %v", setupErr)
	}

	// Контекст теста создаётся ПОСЛЕ подъёма, а не до. Иначе первый тест
	// пакета отдал бы свои 30 секунд на ожидание контейнера и пришёл бы к
	// CREATE DATABASE с уже истёкшим дедлайном — падение выглядело бы как
	// проблема клонирования, хотя контейнер поднялся успешно.
	ctx := stand.Ctx(t)

	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("имя базы: %v", err)
	}
	name := "stand_" + hex.EncodeToString(b)

	if _, err := pool.Exec(ctx,
		fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, templateDB)); err != nil {
		t.Fatalf("клонирование шаблона: %v", err)
	}

	t.Cleanup(func() {
		// WITH (FORCE) обрывает чужие сеансы сам (Postgres 13+). Всесильным
		// он не является: подготовленные транзакции и слоты логической
		// репликации удалить базу всё равно не дадут.
		//
		// Ошибка здесь НЕ глотается: молчаливый провал уборки — это утечка
		// баз, которая проявится через сотню тестов исчерпанным диском, и
		// связать её с причиной будет уже нечем.
		dropCtx, cancel := context.WithTimeout(context.Background(), stand.Timeout)
		defer cancel()
		if _, err := pool.Exec(dropCtx,
			fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name)); err != nil {
			t.Errorf("база %s не удалена: %v", name, err)
		}
	})

	return dsnFor(name)
}

func TestTemplate(t *testing.T) {
	stand.SkipIfShort(t)
	for i := range stand.Cases() {
		t.Run(fmt.Sprintf("case-%02d", i), func(t *testing.T) {
			dsn := newDB(t)
			assertIsolated(t, dsn)
		})
	}
}

// assertIsolated — та же проверка, что в варианте naive. Она здесь не для
// симметрии: именно изоляцию и подозревают, когда контейнер перестаёт быть
// персональным.
func assertIsolated(t *testing.T, dsn string) {
	t.Helper()
	ctx := stand.Ctx(t)

	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	defer p.Close()

	if _, err := p.Exec(ctx,
		`INSERT INTO accounts (email) VALUES ($1)`, "s@example.test"); err != nil {
		t.Fatalf("вставка: %v", err)
	}

	var n int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM accounts`).Scan(&n); err != nil {
		t.Fatalf("счёт: %v", err)
	}
	if n != 1 {
		t.Fatalf("база не изолирована: строк %d, ожидалась 1", n)
	}
}
