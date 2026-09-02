// Команда probe выполняет пробу одной клетки таблицы форматов:
// кодирует каноническую запись схемой писателя, декодирует схемой
// читателя и печатает результат классификации строкой JSON — по строке
// на каждую каноническую запись.
//
// Вызывающая сторона задаёт КООРДИНАТЫ КЛЕТКИ: плечо, изменение,
// направление и вид пробы. Больше ничего. Ни файлов, ни каталога, ни
// данных, ни ожидания — всё это стенд находит и вычисляет сам:
//
//   - схемы ищутся в манифесте стенда по нотации плеча, версии и
//     изменению (internal/stand);
//   - записи берутся из канонического набора той же схемы писателя;
//   - ожидание вычисляется из записи и двух схем (codec.ExpectedRecord)
//     до единого вызова кодека.
//
// Так закрыт класс «исход можно подогнать снаружи», открывавшийся пять
// кругов подряд. Пока данные приходили аргументом, любая клетка
// переводилась в любой исход подбором записи; пока приходили пути —
// подбором имени файла, подставным каталогом или несовпадающим с
// нотацией плечом. Сейчас у вызывающей стороны нет ни одного из этих
// рычагов: координата клетки определяет всё, а содержимое схем и
// записей связано с их именами дайджестами манифеста.
//
// Пять записей на клетку — не избыточность. Тихая порча у protobuf
// невидима ровно тогда, когда истинное значение совпадает с нулевым
// значением типа читателя; одна запись может случайно попасть в эту
// точку и показать «ok» там, где клетка на самом деле «wrong». Пять
// записей делают такое совпадение видимым: строки клетки разойдутся
// между собой.
//
// Языконезависимое описание всех правил стенда — schemas/spec.md.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"

	"tech.khorost/serialization-formats/internal/codec"
	"tech.khorost/serialization-formats/internal/exchange"
	"tech.khorost/serialization-formats/internal/probe"
	"tech.khorost/serialization-formats/internal/stand"
)

// lang зашит в "go": так Python-часть отличает Go-строки от Java-строк
// (Задача 4) в общем потоке результатов, даже если обе реализации пишут
// в один файл. Литералы фиксированы спекой: "go" и "java".
const lang = "go"

// standRoot — каталог стенда. Не аргумент и аргументом быть не может:
// возможность указать каталог означает возможность подсунуть свой, а
// подделка не оставит следа в репозитории, потому что чужой каталог в
// репозиторий не входит.
//
// Значение либо вкомпилировано при сборке
// (-ldflags "-X main.standRoot=<путь>"), либо, если оно пустое, ищется
// от расположения исполняемого файла вверх по каталогам: берётся первый
// каталог, в котором есть schemas/manifest.json. Текущий рабочий
// каталог в поиске НЕ участвует — иначе подставной стенд возвращался бы
// через запуск из чужого места.
var standRoot string

// probeResult — форма ОДНОЙ строки вывода.
//
// Cell — самодостаточный ключ строки (см. stand.CellKey): по нему
// строки склеиваются, и в нём уже есть всё, чем строки одной тройки
// «плечо, изменение, направление» отличаются друг от друга — вид пробы,
// язык и номер записи.
//
// Record и Want печатаются открытым текстом, чтобы строку можно было
// перепроверить, не веря классификации на слово и не открывая
// records.json. У строк «n/a» их нет: там кодек не запускался, и
// печатать нечего.
type probeResult struct {
	Cell        string `json:"cell"`
	Kind        string `json:"kind"`
	Format      string `json:"format"`
	Change      string `json:"change"`
	Direction   string `json:"direction"`
	RecordIndex int    `json:"record_index"`
	Stage       string `json:"stage"`
	Lang        string `json:"lang"`
	Outcome     string `json:"outcome"`
	// Encoded — выполнялось ли фактическое кодирование записи.
	// Без него строки «n/a» завышают счёт настоящих кодирований ровно
	// вдвое, а отличить их от строк, где кодек работал, нечем: нулевой
	// размер бывает и у отказа на кодировании. У круговой пробы признак
	// относится к ПЕРВОМУ кодированию — тому, размер которого попадает
	// в bytes.
	Encoded bool           `json:"encoded"`
	Bytes   int            `json:"bytes"`
	Record  map[string]any `json:"record,omitempty"`
	Want    map[string]any `json:"want,omitempty"`
	// Got — ФАКТИЧЕСКИ прочитанная запись, а не запись писателя и не
	// ожидание. Задача 6: у клетки с исходом «wrong» это единственное
	// поле, которое подтверждает «прочиталось, но не то» наблюдаемым
	// значением — без него утверждение держалось бы на слово
	// классификатору. Заполняется всегда, когда декодирование состоялось
	// (успешно, независимо от исхода ok/wrong); отсутствует, если
	// декодирование не запускалось или отказало (refused/error/n/a) —
	// там и наблюдать нечего.
	Got   map[string]any `json:"got,omitempty"`
	Error string         `json:"error,omitempty"`
	// Writer/Reader — Задача 8, ось перекрёстного чтения. Заполняются
	// ТОЛЬКО у строк kind="cross": Writer — язык, чьи байты приняты через
	// файл обмена (аргумент --writer-lang), Reader — язык ЭТОГО процесса
	// (тот же литерал, что и Lang). У compat/roundtrip/size этих полей
	// нет: там писатель и читатель — один и тот же процесс по построению,
	// и поле было бы избыточным дублем Lang.
	Writer string `json:"writer,omitempty"`
	Reader string `json:"reader,omitempty"`
}

// identityResult — строка вида --op=identity: проба ИДЕНТИЧНОСТИ БАЙТ
// одной реализации самой с собой (§17.6 spec.md, «контроль»). Она НЕ
// говорит, совпадают ли байты МЕЖДУ реализациями — это может решить
// только сравнение SHA256 двух таких строк, снятых у разных языков на
// одном (format, change), и делает это уже разбор (scripts/analyze-cross.py),
// а не сама проба: одна реализация физически не видит байтов другой,
// минуя как раз тот же файловый обмен, ради проверки которого заведена
// эта же ось, — а тут CROSS-обмен не нужен вовсе: для утверждения
// «байты совпадают» достаточно сравнить дайджесты, физическая передача
// самих байтов ничего не добавляет к этому конкретному вопросу.
type identityResult struct {
	Kind         string `json:"kind"` // всегда "identity-probe" — сырая строка ОДНОГО языка, не путать с "identity" — итоговым выводом разбора
	Format       string `json:"format"`
	Change       string `json:"change"`
	Lang         string `json:"lang"`
	ControlEqual bool   `json:"control_equal"`
	SHA256       string `json:"sha256"`
	Bytes        int    `json:"bytes"`
}

// sizeResult — форма строки вида --op=size. Она НЕ переиспользует
// probeResult: у оси размера нет ни исхода, ни стадии, ни ожидания —
// это не сравнение писателя с читателем, а измерение одной схемы, и
// смешивание форм заставило бы читателя гадать, какие поля тут вообще
// имеют смысл.
//
// SchemaBytes без omitempty намеренно: у контрольного плеча он РОВНО
// ноль, и это содержательный ноль — «читателю схема не нужна вовсе», а
// не пропуск значения (см. spec.md, §13 и решение по Задаче 5).
//
// SchemaBytes — вес КАНОНИЧЕСКОЙ формы схемы (основная величина, круг
// ревью 2, находка C2), SchemaFileBytes — вес файла схемы КАК ЕСТЬ.
// Вес «как есть» смешивает три разные единицы — размеченный отступами
// текст (Avro, JSON Schema) против компилированного двоичного файла
// (Protobuf), и первые два платят «налог форматирования», которого
// Protobuf не платит по устройству. Оба числа остаются в строке: раз
// показывает формат без нашего форматирования, второй — во сколько это
// форматирование обходится.
//
// BatchBytes/BatchZstd — свойства КЛЕТКИ целиком (плечо + версия схемы),
// а не отдельной записи: одно и то же значение печатается на каждой из
// пяти строк клетки, тем же принципом, каким клеточные исходы compat/
// roundtrip печатаются на каждой записи (spec.md §3.5, §10.3.2). Они
// отвечают на другой вопрос, чем Zstd: одна запись в 12–56 байт меряет
// только оверхед заголовка кадра zstd, а не собственно сжатие — пачка
// из пяти записей даёт входу размер, на котором разница между форматами
// не тонет в этом оверхеде.
//
// BatchHash — SHA-256 БАЙТОВ пачки, шестнадцатеричной строкой.
// Круг ревью 2, находка M1: в строке лежала ДЛИНА пачки, а не её
// содержимое, поэтому «побайтовое совпадение» между реализациями было
// на самом деле «совпадение длин» — расхождение внутри пачки (круг
// ревью 2, находка C3: недетерминированный порядок полей у protobuf)
// длину не меняло и потому было невидимо тому самому сравнению, которое
// должно было его поймать.
//
// BatchContentHash — SHA-256 СОДЕРЖИМОГО пачки: каждая запись
// расшифровывается обратно той же схемой (direction=same, поэтому это
// чистый круговой прогон без переименований и приведений типа) и
// сериализуется в канонический вид (ключи объекта — по алфавиту), а не
// берётся байтами с провода. Круг ревью 3: JSON, как и Protobuf, не
// имеет канонической формы байт, определённой спецификацией формата —
// порядок ключей объекта в JSON не специфицирован вовсе, и обе
// реализации вправе выбрать свой (одна сортирует по алфавиту, другая
// сохраняет порядок вставки — обе правы). Побайтовое равенство поэтому
// проверяется только там, где формат его ГАРАНТИРУЕТ (Avro — Parsing
// Canonical Form по спецификации; Protobuf — не по спецификации, но
// эмпирически совпадает у обеих реализаций при включённом
// детерминированном кодировании, см. spec.md §10.3.2). Для форматов без
// такой гарантии (json, json-schema) сравнивается СОДЕРЖИМОЕ через это
// поле, а не байты через BatchHash.
type sizeResult struct {
	Cell             string         `json:"cell"`
	Kind             string         `json:"kind"`
	Format           string         `json:"format"`
	Change           string         `json:"change"`
	Direction        string         `json:"direction"`
	RecordIndex      int            `json:"record_index"`
	Lang             string         `json:"lang"`
	Bytes            int            `json:"bytes"`
	Zstd             int            `json:"zstd"`
	SchemaBytes      int            `json:"schema_bytes"`
	SchemaFileBytes  int            `json:"schema_file_bytes"`
	BatchBytes       int            `json:"batch_bytes"`
	BatchZstd        int            `json:"batch_zstd"`
	BatchHash        string         `json:"batch_hash"`
	BatchContentHash string         `json:"batch_content_hash"`
	Record           map[string]any `json:"record,omitempty"`
}

// zstdLevel — уровень сжатия зафиксирован ради воспроизводимости: без
// фиксации сжатый размер зависел бы от версии библиотеки/дефолтов, а не
// от формата. 3 — обычный уровень «по умолчанию» у zstd (не самый
// быстрый и не максимальный), тот, что используют, когда явно не просят
// другого. Java-часть обязана использовать тот же уровень — см.
// zstdLevel в Codec.java и обоснование в spec.md.
const zstdLevel = 3

// compressZstd сжимает РОВНО ТЕ БАЙТЫ, что попали в поле bytes, — не
// перекодированную запись и не что-то ещё. Разница была бы незаметна на
// глаз и подменила бы измеряемое.
func compressZstd(b []byte) (int, error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(zstdLevel)))
	if err != nil {
		return 0, fmt.Errorf("zstd-энкодер: %w", err)
	}
	defer enc.Close()
	return len(enc.EncodeAll(b, nil)), nil
}

// frameBatch склеивает уже закодированные записи клетки в один поток с
// явным обрамлением: 4 байта длины (uint32, big-endian) + сами байты
// записи, подряд, без разделителей. Самодельная рамка стенда (не Avro
// container file, не что-то специфичное для Protobuf) — единая для всех
// четырёх плеч, задокументирована в spec.md §10.3.2 и обязана совпасть
// побайтово у обеих реализаций.
func frameBatch(records [][]byte) []byte {
	var out []byte
	var length [4]byte
	for _, rec := range records {
		binary.BigEndian.PutUint32(length[:], uint32(len(rec)))
		out = append(out, length[:]...)
		out = append(out, rec...)
	}
	return out
}

// validOps — белый список видов пробы: неизвестное значение раньше
// проезжало до конца и печаталось как есть, подменяя клетку в общей
// таблице невесть чем.
// cross-emit/cross-accept/identity — Задача 8, ось перекрёстного чтения
// (spec.md §17): cross-emit отдаёт байты через файл обмена, cross-accept
// принимает чужие и классифицирует их ТОЙ ЖЕ функцией, что и compat;
// identity — контроль байтовой идентичности одной реализации с собой,
// без которого межъязыковое сравнение байтов бессмысленно (§17.6).
var validOps = map[string]bool{
	"compat": true, "roundtrip": true, "size": true,
	"cross-emit": true, "cross-accept": true, "identity": true,
}

// writerLangs — допустимые значения --writer-lang (только для
// --op=cross-accept): язык, чьи байты этот вызов принимает через файл
// обмена.
var writerLangs = map[string]bool{"go": true, "java": true}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run — вход командной строки. Каталог стенда он не выбирает, а узнаёт.
func run(args []string, out io.Writer) error {
	dir, err := resolveStandRoot()
	if err != nil {
		return err
	}
	return runIn(dir, args, out)
}

// runIn — то же самое для заранее известного каталога стенда.
// Неэкспортированная и недостижимая из командной строки: ею
// пользуются только тесты, которым нужен свой временный стенд.
func runIn(standDir string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	format := fs.String("format", "", "плечо кодирования: json | json-schema | avro | protobuf")
	change := fs.String("change", "", "изменение схемы: "+strings.Join(stand.Changes, " | "))
	direction := fs.String("direction", "", "направление: "+strings.Join(stand.Directions, " | "))
	// op="compat" — обычная проба чтения; op="roundtrip" — проверка
	// того, что неизвестные читателю данные переживают повторное
	// кодирование (см. codec.RoundTripper).
	op := fs.String("op", "compat", "вид пробы: compat (чтение) | roundtrip (перенос неизвестных полей) | size (размер, сжатие, вес схемы) | cross-emit | cross-accept | identity")
	// writerLang — только для --op=cross-accept: язык, чьи байты
	// принимаются через файл обмена. Не координата §4.1 (нет в списке
	// аргументов клетки) — это четвёртая координата ИМЕННО этой оси,
	// «кто записал», симметричная тому, что вызывающий язык всегда и
	// есть «кто читает» (spec.md §17.2).
	writerLang := fs.String("writer-lang", "", "язык-писатель для --op=cross-accept: go | java")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !validOps[*op] {
		return fmt.Errorf("--op: неизвестное значение %q, допустимы только %s", *op, strings.Join(sortedKeys(validOps), ", "))
	}
	// Размер и идентичность — свойства ОДНОЙ схемы, а не пары
	// «писатель/читатель»: newer_reader и newer_writer выбирают ДВЕ
	// разные схемы, и вопрос «чья схема» не имел бы однозначного ответа.
	// same — та же запись манифеста для писателя и читателя, ровно то,
	// что нужно.
	if (*op == "size" || *op == "identity") && (*direction == "newer_reader" || *direction == "newer_writer") {
		return fmt.Errorf("--op=%s: направление обязано быть same — измеряется одна схема, а не пара писатель/читатель", *op)
	}
	if *op == "cross-accept" {
		if !writerLangs[*writerLang] {
			return fmt.Errorf("--op=cross-accept: --writer-lang обязателен и допускает только go или java, получено %q", *writerLang)
		}
	} else if strings.TrimSpace(*writerLang) != "" {
		return fmt.Errorf("--writer-lang имеет смысл только при --op=cross-accept")
	}
	for _, required := range []struct{ name, value string }{
		{"--format", *format}, {"--change", *change}, {"--direction", *direction},
	} {
		if strings.TrimSpace(required.value) == "" {
			return fmt.Errorf("%s обязателен", required.name)
		}
	}

	manifest, err := stand.LoadManifest(standDir)
	if err != nil {
		return err
	}

	// Схемы находит стенд, а не вызывающая сторона. Нотация выводится
	// из плеча той же координатой, что выбирает кодек, поэтому
	// «протобуфное плечо со схемами Avro» здесь невыразимо.
	writerEntry, readerEntry, err := manifest.Resolve(*format, *change, *direction)
	if err != nil {
		return err
	}
	writerSchema, err := readSchema(manifest, writerEntry)
	if err != nil {
		return fmt.Errorf("схема писателя: %w", err)
	}
	readerSchema := writerSchema
	if readerEntry.Name != writerEntry.Name {
		readerSchema, err = readSchema(manifest, readerEntry)
		if err != nil {
			return fmt.Errorf("схема читателя: %w", err)
		}
	}

	records, err := manifest.Records(writerEntry)
	if err != nil {
		return err
	}

	c, err := codec.New(*format)
	if err != nil {
		return err
	}

	newRow := func(i int) probeResult {
		return probeResult{
			Cell:        stand.CellKey(lang, *op, *format, *change, *direction, i),
			Kind:        *op,
			Format:      *format,
			Change:      *change,
			Direction:   *direction,
			RecordIndex: i,
			Lang:        lang,
		}
	}

	// Клеточные исходы: то, что решается ПАРОЙ СХЕМ и одинаково для
	// любой записи. Проверяются до того, как записи вообще понадобятся
	// — иначе вырожденная пара (reuse_tag у Avro, где схема читателя
	// структурно совпадает со схемой писателя) споткнулась бы о
	// проверку соответствия записи схеме и превратилась бы из находки
	// стенда в его поломку. Строка всё равно печатается на каждую
	// запись: клетка описывает их все, и таблица остаётся однородной.
	//
	// Порядок здесь — часть контракта (см. spec.md, приоритет причин).
	// Сперва то, что не зависит от схем вовсе: у плеча либо есть
	// непрозрачный остаток, либо нет, и разобранная схема на этот ответ
	// не влияет.
	cellRow := func(outcome, reason string) error {
		return emit(out, records, func(i int) (probeResult, error) {
			res := newRow(i)
			res.Outcome = outcome
			res.Error = reason
			// Ни записи, ни ожидания: кодек не запускался, ожидание не
			// вычислялось. Наличие ожидания в строке означает ровно то,
			// что оно вычислялось, — этот признак надо сохранить.
			return res, nil
		})
	}

	if *op == "roundtrip" {
		if _, ok := c.(codec.RoundTripper); !ok {
			return cellRow("n/a", "формат не переносит данные, неизвестные читателю, через повторное кодирование")
		}
	}

	// Схемы приводятся в рабочий вид ЗАРАНЕЕ — и для расчёта ожидания,
	// и для самого плеча. Круг правок 7: раньше это случалось по
	// дороге, и «сбой пробы» получался разным в зависимости от того,
	// кто споткнулся первым. Теперь неудача подготовки — исход всей
	// клетки целиком.
	for _, schema := range []codec.Schema{writerSchema, readerSchema} {
		err := codec.PrepareSchema(c, schema)
		switch {
		case errors.Is(err, codec.ErrProbeFailure):
			return cellRow("error", err.Error())
		case err != nil:
			return err
		}
	}

	if err := codec.SchemasAreDegenerate(writerSchema, readerSchema); err != nil {
		if !errors.Is(err, codec.ErrDegenerateSchema) {
			return err
		}
		return cellRow("n/a", err.Error())
	}

	if *op == "size" {
		return runSizeCell(out, c, records, writerSchema, lang, *op, *format, *change, *direction)
	}

	// Задача 8, ось перекрёстного чтения (spec.md §17). Три новых вида
	// пробы попадают сюда ПОСЛЕ подготовки схем и проверки вырожденности
	// — те же самые причины n/a/error, что и у compat/roundtrip, обязаны
	// сработать одинаково: клетка, невыразимая в этой нотации, не
	// становится выразимой оттого, что байты идут через файл, а не
	// напрямую.
	if *op == "cross-emit" {
		return runCrossEmit(records, c, writerSchema, lang, *format, *change, *direction)
	}
	if *op == "cross-accept" {
		return runCrossAccept(out, records, c, writerSchema, readerSchema, lang, *writerLang, *op, *format, *change, *direction)
	}
	if *op == "identity" {
		return runIdentity(out, records, c, writerSchema, lang, *format, *change)
	}

	rt, _ := c.(codec.RoundTripper)
	return emit(out, records, func(i int) (probeResult, error) {
		res := newRow(i)
		rec, _ := codec.Normalize(records[i]).(map[string]any)
		if *op == "roundtrip" {
			return res, runRoundTrip(&res, rt, c, rec, writerSchema, readerSchema)
		}
		return res, runCompat(&res, c, rec, writerSchema, readerSchema)
	})
}

// readSchema читает файл схемы РОВНО ОДИН РАЗ, сверяя дайджест по тем
// самым байтам, которые пойдут дальше — в разбор схемы и в кодек.
func readSchema(m *stand.Manifest, e stand.Entry) (codec.Schema, error) {
	raw, err := m.ReadFile(e.Name)
	if err != nil {
		return codec.Schema{}, err
	}
	return codec.Schema{Name: e.Name, Notation: e.Notation, Bytes: raw}, nil
}

// resolveStandRoot определяет каталог стенда, не спрашивая вызывающую
// сторону: либо вкомпилированное значение, либо первый каталог с
// schemas/manifest.json вверх от исполняемого файла.
func resolveStandRoot() (string, error) {
	if strings.TrimSpace(standRoot) != "" {
		return filepath.Join(standRoot, "schemas"), nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("не удалось определить расположение пробы: %w", err)
	}
	dir := filepath.Dir(exe)
	for {
		candidate := filepath.Join(dir, "schemas")
		if _, err := os.Stat(filepath.Join(candidate, stand.ManifestFileName)); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf(
				"каталог стенда не найден: рядом с пробой (%s) и выше нет schemas/%s; "+
					"собери пробу внутри дерева стенда или вкомпилируй путь через -ldflags \"-X main.standRoot=<каталог стенда>\"",
				exe, stand.ManifestFileName)
		}
		dir = parent
	}
}

// emit печатает по строке на каждую каноническую запись. Первая же
// ошибка останавливает вывод целиком: наполовину напечатанная клетка
// хуже ненапечатанной — по ней нельзя понять, чего не хватает.
//
// Обобщена по типу строки: у оси размера (sizeResult) своя форма строки,
// не совпадающая с probeResult (§ комментарий у sizeResult), а правило
// «сперва посчитать все строки, потом печатать» — общее для обеих осей.
func emit[T any](out io.Writer, records []map[string]any, row func(i int) (T, error)) error {
	rows := make([]T, 0, len(records))
	for i := range records {
		res, err := row(i)
		if err != nil {
			return err
		}
		rows = append(rows, res)
	}
	enc := json.NewEncoder(out)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}

// runSizeCell реализует --op=size для ЦЕЛОЙ клетки: кодирует все пять
// канонических записей схемой писателя и измеряет пять чисел —
// поштучный размер записи, поштучный размер под zstd, вес схемы (нужной,
// чтобы прочитать запись), да ещё размер и сжатие ПАЧКИ из всех пяти
// записей клетки с явным обрамлением (spec.md §10.3.2).
//
// Пачка считается ОДИН РАЗ на клетку, а не на запись: batch_bytes и
// batch_zstd — свойства клетки целиком, и печатаются одним и тем же
// значением на все пять строк — тот же принцип, каким клеточные исходы
// compat/roundtrip печатаются на каждой записи (spec.md §3.5).
//
// Ошибка возврата останавливает пробу целиком (как и у compat/roundtrip
// при сбое стенда): канонические записи по построению проходят §8.1, и
// отказ кодирования здесь означал бы, что сломалась проба, а не формат
// — печатать для этого исход в строке нечего, у оси размера его нет.
func runSizeCell(out io.Writer, c codec.Codec, records []map[string]any, writer codec.Schema,
	lang, op, format, change, direction string) error {
	// Кодек детерминирован (одна и та же запись даёт одни и те же байты
	// при каждом вызове), но каждая запись кодируется РОВНО ОДИН РАЗ:
	// эти же байты идут и в bytes/zstd отдельной строки, и в пачку —
	// повторное кодирование для пачки означало бы мерить что-то другое.
	encoded := make([][]byte, len(records))
	for i := range records {
		rec, _ := codec.Normalize(records[i]).(map[string]any)
		b, err := c.Encode(rec, writer)
		if err != nil {
			return fmt.Errorf("--op=size: %s отказался закодировать каноническую запись %d: %w", format, i, err)
		}
		encoded[i] = b
	}

	batch := frameBatch(encoded)
	batchBytes := len(batch)
	batchZstd, err := compressZstd(batch)
	if err != nil {
		return fmt.Errorf("--op=size: сжатие пачки: %w", err)
	}
	// Круг ревью 2, находка M1: в строке раньше лежала ДЛИНА пачки, а
	// не её содержимое — «побайтовое совпадение» между реализациями было
	// на самом деле «совпадение длин», и расхождение содержимого (при
	// той же длине — ровно то, что дал недетерминированный порядок
	// полей у protobuf, находка C3) было бы невидимо этой самой сверке.
	batchSum := sha256.Sum256(batch)
	batchHash := hex.EncodeToString(batchSum[:])

	// Круг ревью 3: у JSON, как и у Protobuf, нет канонической формы
	// байт, определённой спецификацией формата — порядок ключей объекта
	// в JSON вообще не специфицирован, и обе реализации вправе выбрать
	// свой (одна сортирует по алфавиту, другая сохраняет порядок
	// вставки). Побайтовое сравнение (BatchHash) поэтому годится не
	// для всех плеч — а вот содержимое обязано совпасть ВСЕГДА:
	// расшифровываем каждую запись обратно той же схемой (direction=same
	// — чистый круговой прогон без переименований и приведений типа) и
	// хешируем канонический вид результата (ключи по алфавиту — этого
	// добивается сам encoding/json.Marshal для map[string]any).
	decoded := make([]map[string]any, len(records))
	for i := range encoded {
		m, err := c.Decode(encoded[i], writer, writer)
		if err != nil {
			return fmt.Errorf("--op=size: %s отказался расшифровать обратно запись %d для сверки содержимого: %w", format, i, err)
		}
		decoded[i] = m
	}
	canonicalContent, err := json.Marshal(decoded)
	if err != nil {
		return fmt.Errorf("--op=size: сериализация содержимого пачки: %w", err)
	}
	contentSum := sha256.Sum256(canonicalContent)
	batchContentHash := hex.EncodeToString(contentSum[:])

	// Контрольное плечо схему не читает вовсе (spec.md §7.1, §13):
	// читателю она не нужна ни для чего, и вес схемы у него — нуль в
	// обоих измерениях. У остальных трёх плеч SchemaFileBytes — вес
	// файла схемы КАК ОН ЛЕЖИТ на диске, а SchemaBytes — вес его
	// КАНОНИЧЕСКОЙ формы (круг ревью 2, находка C2): raw-вес смешивает
	// текстовое форматирование (Avro/JSON Schema) и компилированный
	// двоичный дескриптор (Protobuf) под одним числом, а каноническая
	// форма эту разницу снимает.
	schemaFileBytes := len(writer.Bytes)
	schemaBytes := schemaFileBytes
	if format == "json" {
		schemaFileBytes = 0
		schemaBytes = 0
	} else if cw, ok := c.(codec.CanonicalWeigher); ok {
		w, err := cw.CanonicalWeight(writer)
		if err != nil {
			return fmt.Errorf("--op=size: вес канонической формы схемы: %w", err)
		}
		schemaBytes = w
	}

	return emit(out, records, func(i int) (sizeResult, error) {
		res := sizeResult{
			Cell:             stand.CellKey(lang, op, format, change, direction, i),
			Kind:             op,
			Format:           format,
			Change:           change,
			Direction:        direction,
			RecordIndex:      i,
			Lang:             lang,
			SchemaBytes:      schemaBytes,
			SchemaFileBytes:  schemaFileBytes,
			BatchBytes:       batchBytes,
			BatchZstd:        batchZstd,
			BatchHash:        batchHash,
			BatchContentHash: batchContentHash,
		}
		rec, _ := codec.Normalize(records[i]).(map[string]any)
		res.Record = rec
		res.Bytes = len(encoded[i])
		// zstd сжимает РОВНО ТЕ БАЙТЫ, что попали в res.Bytes выше, — не
		// запись, перекодированную ещё раз каким-то другим путём.
		z, err := compressZstd(encoded[i])
		if err != nil {
			return res, err
		}
		res.Zstd = z
		return res, nil
	})
}

// runCompat — обычная проба чтения. Ошибка возврата означает сбой
// СТЕНДА (запись не соответствует схеме писателя, схему не разобрать) —
// такое останавливает пробу целиком; всё, что решил формат, ложится в
// res и печатается строкой.
func runCompat(res *probeResult, c codec.Codec, rec map[string]any, writer, reader codec.Schema) error {
	// Ожидание вычисляется ДО единого вызова кодека — эта
	// последовательность и есть гарантия честности: вычисление
	// физически не может увидеть результат декодирования, потому что
	// на этот момент его ещё не существует.
	want, err := codec.ExpectedRecord(rec, writer, reader)
	if err != nil {
		return fmt.Errorf("не удалось вычислить ожидаемую запись: %w", err)
	}
	res.Record = rec
	res.Want = want

	var got map[string]any
	var probeErr error
	res.Stage = "encode"
	b, encErr := c.Encode(rec, writer)
	if encErr != nil {
		probeErr = encErr
	} else {
		res.Encoded = true
		res.Bytes = len(b)
		res.Stage = "decode"
		got, probeErr = c.Decode(b, writer, reader)
	}
	if probeErr == nil && got != nil {
		res.Got = got
	}

	res.Outcome = probe.Classify(got, want, probeErr)
	if probeErr != nil {
		res.Error = probeErr.Error()
	}
	return nil
}

// runRoundTrip реализует --op=roundtrip: писатель знает поле, которого
// не знает читатель; проверяется, что значение этого поля переживает
// декодирование схемой читателя, повторное кодирование ею же и
// финальное чтение схемой писателя.
//
// Ошибки внутри самого прогона — поведение формата и печатаются
// строкой. А вот несоответствие записи схеме писателя — сбой стенда, и
// оно останавливает пробу ровно так же, как в обычном режиме.
func runRoundTrip(res *probeResult, rt codec.RoundTripper, c codec.Codec, rec map[string]any, writer, reader codec.Schema) error {
	// «Честная» запись для итоговой сверки — тождественная: финальное
	// чтение идёт ТОЙ ЖЕ схемой писателя, что и исходная запись, так
	// что переименований и приведений типа тут по построению нет.
	identity, err := codec.ExpectedRecord(rec, writer, writer)
	if err != nil {
		return fmt.Errorf("не удалось вычислить ожидаемую запись: %w", err)
	}

	// Сверка выше ничего не знает про схему читателя — её туда и не
	// передавали. Живой пример поймал ревью круга 3: пара
	// v1 -> v2_retype давала «ok», хотя к сохранению НЕИЗВЕСТНОГО поля
	// это не имеет отношения — просто у Protobuf несовпадение типа
	// провода ТОЖЕ уходит в неизвестные поля и переживает повторное
	// кодирование за компанию. Круговой прогон осмыслен только когда
	// читатель ЧИСТО отбрасывает часть полей писателя.
	reduced, err := codec.ExpectedRecord(rec, writer, reader)
	if err != nil {
		return fmt.Errorf("не удалось проверить пару схем для кругового прогона: %w", err)
	}
	if !isCleanFieldDrop(identity, reduced) {
		// Ни записи, ни ожидания: клетка не измеряется (круг правок 7 —
		// именно эти строки и различали два потока).
		res.Outcome = "n/a"
		res.Error = "схема читателя не сводится к чистому отбрасыванию полей писателя — эта пара схем не проверяет заявленное свойство (сохранение неизвестного читателю поля)"
		return nil
	}
	res.Record = rec
	res.Want = identity

	res.Stage = "encode"
	b, err := c.Encode(rec, writer)
	if err != nil {
		res.Outcome = probe.Classify(nil, identity, err)
		res.Error = err.Error()
		return nil
	}
	// bytes круговой пробы — размер ПЕРВОГО кодирования (запись схемой
	// писателя). Второе кодирование, схемой читателя, размера в строку
	// не отдаёт: сравнивать его не с чем, а два числа под одним именем
	// сделали бы столбец бессмысленным.
	res.Encoded = true
	res.Bytes = len(b)

	res.Stage = "decode"
	stateReduced, state, err := rt.DecodeState(b, writer, reader)
	if err != nil {
		res.Outcome = probe.Classify(nil, identity, err)
		res.Error = err.Error()
		return nil
	}

	res.Stage = "encode-state"
	b2, err := rt.EncodeState(stateReduced, reader, state)
	if err != nil {
		res.Outcome = probe.Classify(nil, identity, err)
		res.Error = err.Error()
		return nil
	}

	res.Stage = "decode-final"
	got, err := c.Decode(b2, reader, writer)
	if err == nil && got != nil {
		res.Got = got
	}
	res.Outcome = probe.Classify(got, identity, err)
	if err != nil {
		res.Error = err.Error()
	}
	return nil
}

// isCleanFieldDrop сообщает, годится ли эта пара схем для кругового
// прогона: identity — запись глазами самого писателя, reduced — та же
// запись глазами читателя. «Чисто» — значит, набор имён в reduced есть
// СОБСТВЕННОЕ ПОДМНОЖЕСТВО набора имён в identity (что-то
// действительно отсутствует у читателя) и при этом каждое оставшееся
// поле совпадает со своим значением в identity: читатель либо не знает
// поле вовсе, либо видит его ТОЧНО так же. Если хоть одно значение
// разошлось — читатель не «не знает» поле, а видит его ИНАЧЕ
// (переименование, смена типа, переиспользованный номер), и это уже
// другая находка, не про сохранение неизвестного.
func isCleanFieldDrop(identity, reduced map[string]any) bool {
	if len(reduced) >= len(identity) {
		return false
	}
	for k, v := range reduced {
		iv, ok := identity[k]
		if !ok || !reflect.DeepEqual(iv, v) {
			return false
		}
	}
	return true
}

// runCrossEmit — «отдать байты» (spec.md §17.2): кодирует каждую
// каноническую запись клетки схемой ПИСАТЕЛЯ и кладёт результат в файл
// обмена рабочего каталога процесса (".") — не каталога стенда и не
// аргумента: вызывающий сценарий сам решает, откуда запускать пробу, а
// имя файла детерминированно выводится из координат, номера записи и
// языка-писателя (exchange.CrossFileName). Ничего не печатает: этот вид
// пробы не производит строку результата, только артефакт для чтения
// другим процессом.
//
// Ошибка кодирования здесь — сбой СТЕНДА, а не находка про формат:
// канонические записи по построению проходят §8.1, и отказ означал бы,
// что сломалась проба (тот же принцип, что и у --op=size).
func runCrossEmit(records []map[string]any, c codec.Codec, writer codec.Schema, lang, format, change, direction string) error {
	for i := range records {
		rec, _ := codec.Normalize(records[i]).(map[string]any)
		b, err := c.Encode(rec, writer)
		if err != nil {
			return fmt.Errorf("--op=cross-emit: %s отказался закодировать каноническую запись %d: %w", format, i, err)
		}
		if err := exchange.WriteCross(".", lang, format, change, direction, i, b); err != nil {
			return fmt.Errorf("--op=cross-emit: %w", err)
		}
	}
	return nil
}

// runCrossAccept — «принять чужие байты» (spec.md §17.2): читает файл
// обмена, записанный языком --writer-lang НА ЭТИХ ЖЕ координатах, и
// классифицирует исход РОВНО ТОЙ ЖЕ функцией probe.Classify, которой
// пользуется обычная проба (runCompat выше) — требование решения
// контроллера Задачи 8, без которого перекрёстная колонка мерила бы не
// то же самое, что колонка эволюции.
//
// Целостность файла обмена (exchange.ReadCross: координаты и дайджест)
// проверяется ДО декодирования; её неудача — сбой ПРОБЫ и останавливает
// вывод целиком (симметрично §12 spec.md), а не печатается строкой: файл
// обмена испорчен или подменён — это наша поломка, не поведение формата.
func runCrossAccept(out io.Writer, records []map[string]any, c codec.Codec, writer, reader codec.Schema,
	lang, writerLang, op, format, change, direction string) error {
	return emit(out, records, func(i int) (probeResult, error) {
		res := probeResult{
			Cell:        stand.CellKey(lang, op, format, change, direction, i),
			Kind:        "cross",
			Format:      format,
			Change:      change,
			Direction:   direction,
			RecordIndex: i,
			Lang:        lang,
			Writer:      writerLang,
			Reader:      lang,
		}
		rec, _ := codec.Normalize(records[i]).(map[string]any)
		// Ожидание считается ИЗ ЛОКАЛЬНОЙ канонической записи и локально
		// прочитанных схем — записи в файле обмена не передаются, только
		// байты: обе реализации уже разделяют один и тот же
		// schemas/records.json (§3.4), передавать его копию заново
		// незачем и негде (координаты — единственная поверхность, §4.2).
		want, err := codec.ExpectedRecord(rec, writer, reader)
		if err != nil {
			return res, fmt.Errorf("не удалось вычислить ожидаемую запись: %w", err)
		}
		res.Record = rec
		res.Want = want

		b, err := exchange.ReadCross(".", writerLang, format, change, direction, i)
		if err != nil {
			return res, fmt.Errorf("--op=cross-accept: %w", err)
		}
		res.Encoded = true
		res.Bytes = len(b)
		res.Stage = "decode"

		got, decErr := c.Decode(b, writer, reader)
		if decErr == nil && got != nil {
			res.Got = got
		}
		res.Outcome = probe.Classify(got, want, decErr)
		if decErr != nil {
			res.Error = decErr.Error()
		}
		return res, nil
	})
}

// runIdentity — контроль байтовой идентичности (spec.md §17.6): кодирует
// каноническую запись №0 схемой писателя ДВАЖДЫ, в этом же процессе, и
// сравнивает результат. Только эта запись выбрана намеренно и одна:
// вопрос контроля — «детерминирован ли кодек вообще», а не «на каких
// записях» — тот же кодек детерминирован (или нет) для любой записи
// одной схемы.
//
// НЕ решает, совпадают ли байты МЕЖДУ Go и Java: одна реализация не
// видит байтов другой без обмена файлами, а для этого конкретного
// вопроса он не нужен — довольно сравнить SHA256, и это делает разбор
// (scripts/analyze-cross.py), а не сама проба.
func runIdentity(out io.Writer, records []map[string]any, c codec.Codec, writer codec.Schema, lang, format, change string) error {
	if len(records) == 0 {
		return fmt.Errorf("--op=identity: нет ни одной канонической записи")
	}
	rec, _ := codec.Normalize(records[0]).(map[string]any)
	b1, err := c.Encode(rec, writer)
	if err != nil {
		return fmt.Errorf("--op=identity: %s отказался закодировать каноническую запись 0: %w", format, err)
	}
	b2, err := c.Encode(rec, writer)
	if err != nil {
		return fmt.Errorf("--op=identity: %s отказался закодировать каноническую запись 0 повторно: %w", format, err)
	}
	sum := sha256.Sum256(b1)
	res := identityResult{
		Kind:         "identity-probe",
		Format:       format,
		Change:       change,
		Lang:         lang,
		ControlEqual: bytes.Equal(b1, b2),
		SHA256:       hex.EncodeToString(sum[:]),
		Bytes:        len(b1),
	}
	return json.NewEncoder(out).Encode(res)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
