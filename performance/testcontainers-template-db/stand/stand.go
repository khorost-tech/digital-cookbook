// Package stand — общая часть двух вариантов стенда: образ, схема «миграций»,
// число случаев, метки контейнеров и общий таймаут операций.
//
// Оба варианта прогоняют ОДИН И ТОТ ЖЕ набор подтестов и одну и ту же схему;
// отличается только то, откуда берётся изолированная база.
package stand

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
)

// Image закреплён по digest, а не плавающим тегом. postgres:16-alpine
// переезжает на новый патч-релиз молча, и тогда замеры разных дней
// сравнивают разные образы. Для стенда, весь смысл которого в сравнении
// чисел, это недопустимо. Обновлять осознанно, вместе с перезамером.
const Image = "postgres@sha256:4327b9fd295502f326f44153a1045a7170ddbfffed1c3829798328556cfd09e2"

// Метки контейнеров. StandLabel общая для всех копий стенда, RunLabel —
// своя у каждого запуска run.sh: без неё два параллельных прогона удаляли бы
// контейнеры друг друга, и «убираем только своё» было бы неправдой.
const (
	StandLabelKey = "tech.khorost.stand"
	StandLabelVal = "testcontainers-template-db"
	RunLabelKey   = "tech.khorost.run"
)

// RunID — идентификатор запуска из окружения. Пусто, если тесты запущены
// напрямую, без run.sh: тогда метка запуска не ставится, а уборка по паре
// меток просто ничего не найдёт — это безопаснее, чем удалять по одной.
func RunID() string { return os.Getenv("STAND_RUN") }

// WithLabels вешает на контейнер метку стенда и, если задан, метку запуска.
func WithLabels() testcontainers.CustomizeRequestOption {
	return func(req *testcontainers.GenericContainerRequest) error {
		if req.Labels == nil {
			req.Labels = map[string]string{}
		}
		req.Labels[StandLabelKey] = StandLabelVal
		if id := RunID(); id != "" {
			req.Labels[RunLabelKey] = id
		}
		return nil
	}
}

// Timeout — потолок на операцию с базой. context.Background() без дедлайна
// в тесте означает, что подвисшая операция превращается в подвисший прогон
// без диагностики: тест умрёт по общему таймауту go test, показав панику
// планировщика вместо места отказа.
const Timeout = 30 * time.Second

// StartupTimeout — бюджет на подъём контейнера. Он ЗАВЕДОМО больше, чем
// WithStartupTimeout стратегии ожидания (60 s), и это не запас на всякий
// случай: истеки контекст раньше стратегии — отказ пришёл бы как «context
// deadline exceeded» из библиотеки, а не как «контейнер не открыл порт за
// 60 s». Стенд воспроизводил бы не тот отказ, ради которого написан, и лог
// вводил бы в заблуждение. Проверено: с общим 30-секундным контекстом ровно
// это и получалось.
const StartupTimeout = 90 * time.Second

// Ctx — дедлайн на операции с базой, привязанный к жизни теста.
func Ctx(t *testing.T) context.Context { return ctxWith(t, Timeout) }

// StartCtx — дедлайн на подъём контейнера.
func StartCtx(t *testing.T) context.Context { return ctxWith(t, StartupTimeout) }

func ctxWith(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

// Schema — «миграции» стенда. Намеренно не пустая: часть стоимости варианта
// «контейнер на тест» — это повторный накат схемы, и на пустой схеме разница
// была бы приукрашена.
const Schema = `
CREATE TABLE accounts (
    id         BIGSERIAL PRIMARY KEY,
    email      TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE courses (
    id    TEXT PRIMARY KEY,
    title TEXT NOT NULL
);

CREATE TABLE enrollments (
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    course_id  TEXT   NOT NULL REFERENCES courses(id)  ON DELETE CASCADE,
    grade      INT,
    PRIMARY KEY (account_id, course_id)
);

CREATE INDEX idx_enrollments_course ON enrollments (course_id);
`

// SkipIfShort — интеграционные тесты пропускаются в -short, как и положено
// тестам, которым нужен Docker. Статья это утверждает; стенд обязан вести
// себя так же, иначе утверждение расходится с примером.
func SkipIfShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("интеграционный тест: нужен Docker; пропущен в -short")
	}
}

// Cases — сколько изолированных баз просит стенд. По умолчанию 20: хватает,
// чтобы разница была очевидна, и не хватает, чтобы наивный вариант шёл
// полчаса. Разница линейна по числу случаев — умножайте на своё.
func Cases() int {
	if v := os.Getenv("STAND_CASES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 20
}

// LeakContainers — режим воспроизведения накопления контейнеров: наивный
// вариант перестаёт звать уборку, и контейнеры остаются жить до реапера.
//
// По умолчанию ВЫКЛЮЧЕН: замер скорости не должен зависеть от того, сколько
// мусора остаётся, иначе два варианта меряются в разных условиях.
func LeakContainers() bool { return os.Getenv("STAND_LEAK") == "1" }
