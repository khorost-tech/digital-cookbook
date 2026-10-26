package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ProjectBatch применяет пачку событий и двигает чекпоинт В ТОЙ ЖЕ транзакции.
// Порознь их делать нельзя: упав между применением и сдвигом чекпоинта,
// проектор при рестарте применит те же события повторно.
//
// Атомарность применения и чекпоинта делает повторную доставку одного и
// того же события НЕВОЗМОЖНОЙ в штатном режиме: откат транзакции откатывает
// оба эффекта разом (и запись в balances, и сдвиг checkpoints), следующий
// запуск читает диапазон ЗА чекпоинтом заново с чистого места — повторить
// событие, которого этот запуск ещё не видел, неоткуда.
//
// Курсор здесь — ПАРА (xid, global_pos), а не один global_pos: последний
// выдаётся сиквенсом ДО коммита и потому не является порядком коммитов, а
// курсор по нему теряет события задержавшихся транзакций. Механика и
// воспроизведение — в sql/01-schema.sql, scripts/07-projector-safety.sh и
// FIXTURES.md, «Артефакт 03».
//
// Guard `version = $2 - 1` в UPDATE — не страховка от штатного повтора.
// Против неоднозначного коммита (транзакция зафиксировалась на сервере, но
// клиент не получил подтверждение из-за обрыва) он тоже почти не нужен:
// раз коммит прошёл, вместе с балансом зафиксировался и сдвиг чекпоинта, и
// повторный вызов ProjectBatch прочитает уже НОВУЮ позицию — те же события
// он попросту не выберет.
//
// Реальный сценарий у guard'а один: чекпоинт откатили назад руками (сброс
// после инцидента, восстановление checkpoints из бэкапа отдельно от таблицы
// проекции, ручной UPDATE при разборе). Тогда события ДЕЙСТВИТЕЛЬНО придут
// в UPDATE второй раз, и без guard'а баланс удвоился бы. Второй, более
// узкий: две копии проектора под одним именем — но там гонку в общем случае
// закрывает не guard, а блокировка строки checkpoints.
//
// ProjectBatch — частный случай ProjectBatchInto с таблицей "balances":
// оставлен отдельной функцией ради существующих вызывающих (runProject,
// scripts/03-projections.sh), которые не знают про шаг 6 (blue-green) и не
// должны его касаться.
func ProjectBatch(ctx context.Context, pool *pgxpool.Pool, projector string, limit int) (int, error) {
	return ProjectBatchInto(ctx, pool, projector, "balances", limit)
}

// ProjectBatchInto — то же самое применение пачки, что и ProjectBatch, но в
// ПРОИЗВОЛЬНУЮ таблицу проекции (table) под ОТДЕЛЬНЫМ чекпоинтом (projector).
// Нужна задаче 6 (blue-green): shadow-пересборка (`rebuild-bluegreen`) ведёт
// свою собственную таблицу (`balances_shadow`) и свой собственный чекпоинт
// (`balances-shadow`), не трогая боевую проекцию, пока не готова атомарно
// подменить её.
//
// table подставляется в SQL через fmt.Sprintf, а не через параметр запроса
// ($N) — Postgres не даёт параметризовать имя таблицы плейсхолдером. Это
// безопасно ЗДЕСЬ ТОЛЬКО потому, что table приходит из фиксированного
// набора констант этого стенда (`"balances"` / `shadowTableName`), а не из
// пользовательского ввода — конкатенация произвольной строки была бы SQL-
// инъекцией, но её источник закрыт на уровне вызывающего кода.
func ProjectBatchInto(ctx context.Context, pool *pgxpool.Pool, projector, table string, limit int) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Водяной знак берётся ПЕРВЫМ запросом транзакции — до любой записи.
	// Порядок принципиален: как только транзакция что-нибудь пишет, ей
	// выдаётся собственный xid, и она сама попадает в снимок; xmin тогда
	// оказался бы не больше нашего же xid, и ни одно событие не прошло бы
	// фильтр ниже. Пока мы только читаем, xid нам не выдан.
	//
	// pg_snapshot_xmin — наименьший ещё НЕ завершившийся xid. Всё, что
	// строго меньше, уже либо закоммичено, либо откатано; появиться задним
	// числом оттуда нечему.
	var watermark string
	if err := tx.QueryRow(ctx,
		`SELECT pg_snapshot_xmin(pg_current_snapshot())::text`).Scan(&watermark); err != nil {
		return 0, err
	}

	var pos int64
	var curXid string
	if err := tx.QueryRow(ctx,
		`INSERT INTO checkpoints (projector, global_pos, xid) VALUES ($1, 0, '0'::xid8)
		 ON CONFLICT (projector) DO UPDATE SET projector = EXCLUDED.projector
		 RETURNING global_pos, xid::text`, projector).Scan(&pos, &curXid); err != nil {
		return 0, err
	}

	// Курсор — ПАРА (xid, global_pos), см. sql/01-schema.sql. Одного
	// global_pos недостаточно: он выдаётся до коммита и потому не является
	// порядком коммитов.
	// event_type забирается ВМЕСТЕ с data, хотя решение принимается по
	// data->>'type': ниже они сверяются между собой. Тип события хранится
	// дважды, и два источника истины расходятся тем охотнее, чем дольше
	// живёт стор.
	rows, err := tx.Query(ctx,
		`SELECT global_pos, stream_id, version, data, xid::text, event_type FROM events
		 WHERE (xid > $1::xid8 OR (xid = $1::xid8 AND global_pos > $2))
		   AND xid < $3::xid8
		 ORDER BY xid, global_pos LIMIT $4`, curXid, pos, watermark, limit)
	if err != nil {
		return 0, err
	}

	skipped := 0

	type rec struct {
		pos       int64
		stream    string
		version   int64
		payload   []byte
		xid       string
		eventType string
	}
	var batch []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.pos, &r.stream, &r.version, &r.payload, &r.xid, &r.eventType); err != nil {
			rows.Close()
			return 0, err
		}
		batch = append(batch, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(batch) == 0 {
		return 0, tx.Commit(ctx)
	}

	for _, r := range batch {
		var e Event
		if err := json.Unmarshal(r.payload, &e); err != nil {
			return 0, err
		}
		// Два источника типа обязаны совпадать.
		//
		// Ограничение events_type_matches_payload (sql/01-schema.sql) делает
		// расхождение незаписываемым, и в норме эта проверка не срабатывает
		// никогда. Она стоит здесь не для нормы: append-only стор переживает
		// схемы, которые его наполняли, и строки, записанные ДО появления
		// ограничения, останутся в нём навсегда. Читатель обязан проверять
		// то, на чём строит решение, а не полагаться на то, что писатель был
		// аккуратен во всех прошлых версиях кода.
		//
		// Проверено падающим: с временно снятым ограничением строка
		// event_type='OxygenConsumed' + data.type='OxygenAdded' роняет
		// проектор здесь (см. scripts/07-projector-safety.sh, случай D).
		if e.Type != r.eventType {
			return 0, fmt.Errorf(
				"проекция %s, стрим %s версия %d (global_pos %d): тип события противоречив — "+
					"столбец event_type=%q, а data->>'type'=%q; какой из них правда, "+
					"проектор решать не вправе", table, r.stream, r.version, r.pos, r.eventType, e.Type)
		}

		// FAIL-CLOSED на неизвестном типе события.
		//
		// Раньше здесь не было ветки default, и `delta` оставалась нулём:
		// проектор применял «ничего», повышал версию строки и двигал
		// чекпоинт. Внешнее ревью показало это контрпримером —
		// OxygenLeaked{liters:99} дал ops=1, errors=0, liters=0 и
		// diff_rows=0 на сверке. То есть событие терялось, а обе проверки
		// СОГЛАСОВАННО объявляли потерю корректным состоянием: сверка
		// сворачивала события тем же CASE ... ELSE 0 и приходила к тому же
		// нулю. Две проверки, ошибающиеся одинаково, не ловят ничего.
		//
		// Молчать здесь нельзя ни при каком раскладе: неизвестный тип — это
		// либо порча данных, либо событие от более новой версии кода, и оба
		// случая требуют остановки, а не тихого пропуска. Транзакция
		// откатывается по defer, чекпоинт остаётся на месте.
		var delta float64
		switch e.Type {
		case "OxygenAdded":
			delta = e.Liters
		case "OxygenConsumed":
			delta = -e.Liters
		case "TankRegistered":
			delta = 0 // регистрация не меняет остаток — единственный законный ноль
		default:
			return 0, fmt.Errorf(
				"проекция %s, стрим %s версия %d (global_pos %d): неизвестный тип события %q — "+
					"проектор не знает, как его применить, и останавливается вместо тихого пропуска",
				table, r.stream, r.version, r.pos, e.Type)
		}
		// Сначала UPDATE с guard'ом по версии, и только потом — разбор, если
		// он никого не задел.
		//
		// Раньше здесь стоял INSERT ... ON CONFLICT DO UPDATE ... WHERE
		// version = $2 - 1, и у него была дыра, которую нашло ревью: ветка
		// INSERT безусловна. Если строки проекции ещё нет, а первым пришло
		// событие версии 20, INSERT спокойно создавал строку СРАЗУ на
		// версии 20 — RowsAffected = 1, диагностика ниже не запускалась
		// вовсе, девятнадцать событий пропадали молча. Insert законен
		// РОВНО для версии 1; всё остальное — разрыв.
		upd := fmt.Sprintf(
			`UPDATE %s SET liters = liters + $3, version = $2
			 WHERE stream_id = $1 AND version = $2 - 1`, table)
		tag, err := tx.Exec(ctx, upd, r.stream, r.version, delta)
		if err != nil {
			return 0, err
		}
		if tag.RowsAffected() == 0 {
			// Ноль изменённых строк — единственное место, где проектор может
			// потерять событие бесшумно. Разбираем ВСЕ случаи поимённо.
			var have int64
			err := tx.QueryRow(ctx,
				fmt.Sprintf(`SELECT version FROM %s WHERE stream_id = $1`, table),
				r.stream).Scan(&have)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				// Стрима в проекции нет вовсе.
				if r.version != 1 {
					return 0, fmt.Errorf(
						"разрыв версий в проекции %s: стрима %s в проекции нет, а применяется версия %d — "+
							"перед ним потеряно %d событий", table, r.stream, r.version, r.version-1)
				}
				ins := fmt.Sprintf(
					`INSERT INTO %s (stream_id, version, liters) VALUES ($1, $2, $3)`, table)
				if _, err := tx.Exec(ctx, ins, r.stream, r.version, delta); err != nil {
					return 0, err
				}
			case err != nil:
				return 0, fmt.Errorf("стрим %s версия %d: строка не изменилась и не читается: %w",
					r.stream, r.version, err)
			case have >= r.version:
				// Штатный повтор: событие уже учтено, применять нечего.
				skipped++
			case have == r.version-1:
				// Версия подходит под guard, но UPDATE никого не задел —
				// значит строку изменили между UPDATE и этим SELECT. В
				// однопроцессной схеме стенда это невозможно; если случилось,
				// молчать нельзя.
				return 0, fmt.Errorf(
					"проекция %s, стрим %s: строка на версии %d не изменилась, хотя версия подходит "+
						"под применение версии %d — конкурентная модификация", table, r.stream, have, r.version)
			default:
				// have < r.version-1 — разрыв: между проекцией и применяемым
				// событием потеряно хотя бы одно. Дальше идти нельзя — иначе
				// чекпоинт уедет за пропуск. Транзакция откатывается по defer,
				// чекпоинт остаётся на месте, следующий запуск начнёт с той же
				// позиции.
				return 0, fmt.Errorf(
					"разрыв версий в проекции %s: стрим %s на версии %d, применяется версия %d — "+
						"между ними потеряно %d событий", table, r.stream, have, r.version, r.version-have-1)
			}
		}
		pos = r.pos
		curXid = r.xid
	}
	if skipped > 0 {
		// Не ошибка: повтор — законный исход при неоднозначном коммите или
		// откате чекпоинта назад. Но и не то, о чём стоит молчать.
		fmt.Fprintf(os.Stderr, "projector %s: пропущено как уже применённое: %d из %d\n",
			projector, skipped, len(batch))
	}

	if _, err := tx.Exec(ctx,
		`UPDATE checkpoints SET global_pos = $2, xid = $3::xid8 WHERE projector = $1`,
		projector, pos, curXid); err != nil {
		return 0, err
	}
	return len(batch), tx.Commit(ctx)
}
