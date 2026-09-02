// Package exchange реализует передачу байтов между двумя реализациями
// пробы через файлы рабочего каталога прогона (Задача 8: ось
// перекрёстного чтения — schemas/spec.md, раздел «Ось 4»).
//
// Одна реализация не может декодировать байты, которые закодировала
// другая, внутри одного процесса: перекрёстная проверка требует, чтобы
// одна реализация ЗАПИСАЛА байты на диск, а другая — независимо от
// первой — их ПРОЧИТАЛА. Каталог обмена — не аргумент пробы (§4.2 не
// нарушается): это ТЕКУЩИЙ РАБОЧИЙ КАТАЛОГ ПРОЦЕССА, который выбирает
// вызывающий сценарий, а имя файла внутри него полностью выводится из
// координат клетки, номера записи и языка-писателя — не из содержимого
// и не из произвольного счётчика.
//
// Три ловушки, которые обязан ловить этот пакет (решение контроллера
// Задачи 8): перепутанное имя (координаты внутри файла не совпадают с
// запрошенными), недописанный или испорченный файл (дайджест не сходится
// с содержимым) и запись, оставшаяся от прошлого прогона (это уже забота
// вызывающего сценария — каталог обмена обязан быть очищен ДО прогона,
// см. bench/run-cross.sh; сам пакет очисткой не занимается, потому что
// не может отличить «стухший» файл от свежего с теми же координатами).
package exchange

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// crossEnvelope — самоописывающийся конверт с байтами одной канонической
// записи. Поля Format/Change/Direction/RecordIndex/Lang дублируют то, что
// уже зашито в имени файла, — не для экономии, а потому что имя файла
// проверяет только то, ПО КАКОМУ ПУТИ прочитан файл, а конверт проверяет,
// ЧТО в нём реально лежит. Расхождение между этими двумя источниками и
// есть «перепутанное имя».
type crossEnvelope struct {
	Lang        string `json:"lang"` // язык-писатель
	Format      string `json:"format"`
	Change      string `json:"change"`
	Direction   string `json:"direction"`
	RecordIndex int    `json:"record_index"`
	SHA256      string `json:"sha256"`    // дайджест байтов ДО base64
	BytesB64    string `json:"bytes_b64"` // сами байты, закодированные в base64 ради текстового контейнера
}

// CrossFileName — детерминированное имя файла обмена: функция только от
// координат клетки, номера записи и языка-писателя. Один и тот же вызов
// с одними и теми же аргументами в любой реализации обязан дать одно и
// то же имя — иначе писатель и читатель не найдут друг друга.
func CrossFileName(dir, lang, format, change, direction string, recordIndex int) string {
	name := fmt.Sprintf("cross_%s_%s_%s_%s_%d.json", lang, format, change, direction, recordIndex)
	return filepath.Join(dir, name)
}

// WriteCross кодирует байты в конверт и пишет его АТОМАРНО: сначала во
// временный файл в том же каталоге, затем переименовывает. Не пишущий
// напрямую в целевое имя, обрыв процесса посередине записи не оставит
// недописанный файл ПОД ОЖИДАЕМЫМ ИМЕНЕМ — ReadCross либо увидит целый
// файл, либо не увидит никакого.
func WriteCross(dir, lang, format, change, direction string, recordIndex int, b []byte) error {
	sum := sha256.Sum256(b)
	env := crossEnvelope{
		Lang:        lang,
		Format:      format,
		Change:      change,
		Direction:   direction,
		RecordIndex: recordIndex,
		SHA256:      hex.EncodeToString(sum[:]),
		BytesB64:    base64.StdEncoding.EncodeToString(b),
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("exchange: конверт не сериализуется: %w", err)
	}

	target := CrossFileName(dir, lang, format, change, direction, recordIndex)
	tmp, err := os.CreateTemp(dir, "cross-*.tmp")
	if err != nil {
		return fmt.Errorf("exchange: временный файл обмена: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("exchange: запись временного файла обмена: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("exchange: закрытие временного файла обмена: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("exchange: переименование файла обмена: %w", err)
	}
	return nil
}

// ReadCross читает файл обмена по детерминированному имени, выведенному
// ИЗ ТЕХ ЖЕ координат, и возвращает байты только после того, как:
//  1. файл найден и разобран как конверт;
//  2. координаты ВНУТРИ конверта совпадают с запрошенными (лечит
//     перепутанное имя: содержимое не то, что ожидает читатель, даже
//     если оно лежит по правильному пути);
//  3. дайджест содержимого совпадает с зафиксированным при записи (лечит
//     недописанный или испорченный файл).
//
// Ни один шаг не пропускается ради скорости: файл обмена — единственная
// связь между двумя независимыми процессами, и она не защищена ничем,
// кроме этой проверки.
func ReadCross(dir, lang, format, change, direction string, recordIndex int) ([]byte, error) {
	path := CrossFileName(dir, lang, format, change, direction, recordIndex)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("exchange: файл обмена %s не прочитан: %w", path, err)
	}
	var env crossEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("exchange: файл обмена %s повреждён: не разбирается как JSON: %w", path, err)
	}
	if env.Lang != lang || env.Format != format || env.Change != change ||
		env.Direction != direction || env.RecordIndex != recordIndex {
		return nil, fmt.Errorf(
			"exchange: координаты внутри файла обмена %s не совпадают с запрошенными "+
				"(лежит: lang=%s format=%s change=%s direction=%s record_index=%d; "+
				"ожидались: lang=%s format=%s change=%s direction=%s record_index=%d) — "+
				"файл подменён или взят не тот",
			path, env.Lang, env.Format, env.Change, env.Direction, env.RecordIndex,
			lang, format, change, direction, recordIndex)
	}
	b, err := base64.StdEncoding.DecodeString(env.BytesB64)
	if err != nil {
		return nil, fmt.Errorf("exchange: файл обмена %s повреждён: base64 не разбирается: %w", path, err)
	}
	sum := sha256.Sum256(b)
	got := hex.EncodeToString(sum[:])
	if got != env.SHA256 {
		return nil, fmt.Errorf(
			"exchange: файл обмена %s повреждён или недописан: дайджест не совпадает "+
				"(записан %s, посчитан %s)", path, env.SHA256, got)
	}
	return b, nil
}
