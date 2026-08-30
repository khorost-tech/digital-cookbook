// Command walconsumer подключается к логическому слоту репликации PostgreSQL
// (плагин вывода pgoutput) и печатает декодированные изменения WAL по мере их
// поступления. Используется как живой стенд к статье о WAL: репликация, CDC, PITR.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

const (
	slotName = "wal_slot"
	pubName  = "wal_pub"

	// standbyMessageTimeout — как часто шлём подтверждение позиции серверу,
	// даже если он явно не просил (ReplyRequested), чтобы слот не считался "мёртвым".
	standbyMessageTimeout = 10 * time.Second
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dsn := os.Getenv("WAL_DSN")
	if dsn == "" {
		dsn = "postgres://postgres:waldemo@localhost:5433/waldemo?replication=database"
	}

	conn, err := pgconn.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)

	sysident, err := pglogrepl.IdentifySystem(ctx, conn)
	if err != nil {
		return fmt.Errorf("IDENTIFY_SYSTEM: %w", err)
	}
	log.Printf("system identify: systemID=%s timeline=%d xlogpos=%s dbname=%s",
		sysident.SystemID, sysident.Timeline, sysident.XLogPos, sysident.DBName)

	err = pglogrepl.StartReplication(ctx, conn, slotName, sysident.XLogPos, pglogrepl.StartReplicationOptions{
		PluginArgs: []string{"proto_version '1'", fmt.Sprintf("publication_names '%s'", pubName)},
	})
	if err != nil {
		return fmt.Errorf("START_REPLICATION: %w", err)
	}
	log.Printf("logical replication started: slot=%s publication=%s startLSN=%s", slotName, pubName, sysident.XLogPos)

	clientXLogPos := sysident.XLogPos
	nextStandbyMessageDeadline := time.Now().Add(standbyMessageTimeout)

	// relations хранит последнее полученное описание таблицы (RelationMessage)
	// по RelationID — pgoutput шлёт его перед первым Insert/Update/Delete по
	// таблице (и повторно, если схема меняется), поэтому и Insert/Update
	// нужно резолвить имя таблицы/колонок через эту map.
	relations := map[uint32]*pglogrepl.RelationMessage{}

	for {
		if time.Now().After(nextStandbyMessageDeadline) {
			if err := sendStandbyStatusUpdate(ctx, conn, clientXLogPos); err != nil {
				return fmt.Errorf("send standby status update: %w", err)
			}
			nextStandbyMessageDeadline = time.Now().Add(standbyMessageTimeout)
		}

		// Дедлайн приёма = ближайший момент, когда всё равно нужно слать
		// standby-update — так таймер отправляется точно по расписанию,
		// даже если сервер молчит и keepalive не приходит.
		ctx2, cancel := context.WithDeadline(ctx, nextStandbyMessageDeadline)
		rawMsg, err := conn.ReceiveMessage(ctx2)
		cancel()
		if err != nil {
			if pgconn.Timeout(err) {
				continue // не ошибка: пора отправить standby-update на следующей итерации
			}
			if ctx.Err() != nil {
				return nil // штатное завершение по сигналу/родительскому контексту
			}
			return fmt.Errorf("ReceiveMessage: %w", err)
		}

		var copyData *pgproto3.CopyData
		switch m := rawMsg.(type) {
		case *pgproto3.CopyData:
			copyData = m
		case *pgproto3.ErrorResponse:
			// сервер явно сообщил об ошибке репликации — нельзя терять причину
			// сбоя за общим логом "unexpected message"
			return fmt.Errorf("сервер репликации вернул ошибку: severity=%s code=%s message=%s",
				m.Severity, m.Code, m.Message)
		default:
			log.Printf("unexpected message: %T", rawMsg)
			continue
		}

		switch copyData.Data[0] {
		case pglogrepl.PrimaryKeepaliveMessageByteID:
			pkm, err := pglogrepl.ParsePrimaryKeepaliveMessage(copyData.Data[1:])
			if err != nil {
				return fmt.Errorf("ParsePrimaryKeepaliveMessage: %w", err)
			}
			if pkm.ServerWALEnd > clientXLogPos {
				clientXLogPos = pkm.ServerWALEnd
			}
			if pkm.ReplyRequested {
				if err := sendStandbyStatusUpdate(ctx, conn, clientXLogPos); err != nil {
					return fmt.Errorf("send standby status update (reply requested): %w", err)
				}
				nextStandbyMessageDeadline = time.Now().Add(standbyMessageTimeout)
			}

		case pglogrepl.XLogDataByteID:
			xld, err := pglogrepl.ParseXLogData(copyData.Data[1:])
			if err != nil {
				return fmt.Errorf("ParseXLogData: %w", err)
			}
			if xld.WALStart+pglogrepl.LSN(len(xld.WALData)) > clientXLogPos {
				clientXLogPos = xld.WALStart + pglogrepl.LSN(len(xld.WALData))
			}

			msg, err := pglogrepl.Parse(xld.WALData)
			if err != nil {
				return fmt.Errorf("pglogrepl.Parse: %w", err)
			}
			handleMessage(msg, relations)
		}
	}
}

// handleMessage декодирует одно логическое сообщение pgoutput и печатает
// по одной строке для Insert/Update (тип + таблица + значения колонок).
// Relation-сообщения только пополняют кеш имён таблиц/колонок — pgoutput
// присылает их перед первым изменением по таблице в рамках сессии.
func handleMessage(msg pglogrepl.Message, relations map[uint32]*pglogrepl.RelationMessage) {
	switch m := msg.(type) {
	case *pglogrepl.RelationMessage:
		relations[m.RelationID] = m
		log.Printf("Relation: %s.%s (columns: %s)", m.Namespace, m.RelationName, columnNames(m.Columns))

	case *pglogrepl.BeginMessage:
		// начало транзакции — в демо не печатаем построчно, чтобы не шуметь

	case *pglogrepl.CommitMessage:
		// конец транзакции — аналогично, опускаем

	case *pglogrepl.InsertMessage:
		rel, ok := relations[m.RelationID]
		if !ok {
			log.Printf("Insert: unknown relation id=%d (Relation message not seen yet)", m.RelationID)
			return
		}
		fmt.Printf("Insert %s.%s: %s\n", rel.Namespace, rel.RelationName, formatTuple(rel, m.Tuple))

	case *pglogrepl.UpdateMessage:
		rel, ok := relations[m.RelationID]
		if !ok {
			log.Printf("Update: unknown relation id=%d (Relation message not seen yet)", m.RelationID)
			return
		}
		fmt.Printf("Update %s.%s: %s\n", rel.Namespace, rel.RelationName, formatTuple(rel, m.NewTuple))

	case *pglogrepl.DeleteMessage:
		rel, ok := relations[m.RelationID]
		if !ok {
			log.Printf("Delete: unknown relation id=%d (Relation message not seen yet)", m.RelationID)
			return
		}
		fmt.Printf("Delete %s.%s: %s\n", rel.Namespace, rel.RelationName, formatTuple(rel, m.OldTuple))

	default:
		log.Printf("other message: %T", msg)
	}
}

// formatTuple рендерит колонки TupleData как "col=value, col=value", используя
// имена колонок из RelationMessage. Тип данных 't' — текстовое представление
// значения (единственный формат, который присылает pgoutput в proto_version 1).
func formatTuple(rel *pglogrepl.RelationMessage, tuple *pglogrepl.TupleData) string {
	if tuple == nil {
		return "(no tuple data)"
	}
	parts := make([]string, 0, len(tuple.Columns))
	for i, col := range tuple.Columns {
		name := fmt.Sprintf("col%d", i)
		if i < len(rel.Columns) {
			name = rel.Columns[i].Name
		}
		var value string
		switch col.DataType {
		case pglogrepl.TupleDataTypeText:
			value = string(col.Data)
		case pglogrepl.TupleDataTypeNull:
			value = "NULL"
		case pglogrepl.TupleDataTypeToast:
			value = "(unchanged TOAST)"
		default:
			value = fmt.Sprintf("(binary %d bytes)", len(col.Data))
		}
		parts = append(parts, fmt.Sprintf("%s=%s", name, value))
	}
	return strings.Join(parts, ", ")
}

func columnNames(cols []*pglogrepl.RelationMessageColumn) string {
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}

func sendStandbyStatusUpdate(ctx context.Context, conn *pgconn.PgConn, pos pglogrepl.LSN) error {
	return pglogrepl.SendStandbyStatusUpdate(ctx, conn, pglogrepl.StandbyStatusUpdate{
		WALWritePosition: pos,
	})
}
