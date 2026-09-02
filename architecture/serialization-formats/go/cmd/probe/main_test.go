package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"tech.khorost/serialization-formats/internal/stand"
)

// thisFileDir — каталог этого файла, вычисленный через runtime.Caller, а
// не через os.Getwd(): тесты Задачи 8 переключают текущий рабочий
// каталог процесса (t.Chdir) на временный каталог обмена, и относительный
// путь через "текущий каталог" после этого указывал бы уже не туда.
func thisFileDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(file)
}

func schemasDir(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join(thisFileDir(), "..", "..", "..", "schemas"))
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	return p
}

func schemaFile(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(schemasDir(t), name)
}

// probeLines гоняет пробу на НАСТОЯЩЕМ стенде по координатам клетки.
func probeLines(t *testing.T, args ...string) []probeResult {
	t.Helper()
	return linesIn(t, schemasDir(t), args...)
}

func linesIn(t *testing.T, dir string, args ...string) []probeResult {
	t.Helper()
	var buf bytes.Buffer
	if err := runIn(dir, args, &buf); err != nil {
		t.Fatalf("runIn(%v): %v", args, err)
	}
	return parseLines(t, buf.String())
}

func parseLines(t *testing.T, s string) []probeResult {
	t.Helper()
	var out []probeResult
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var res probeResult
		if err := json.Unmarshal([]byte(line), &res); err != nil {
			t.Fatalf("строка вывода — не валидный JSON: %v\nстрока: %s", err, line)
		}
		out = append(out, res)
	}
	return out
}

func cell(format, change, direction string) []string {
	return []string{"--format=" + format, "--change=" + change, "--direction=" + direction}
}

// ГЛАВНАЯ ПРАВКА КРУГА 6. Проба принимает КООРДИНАТЫ КЛЕТКИ. Путей у неё
// в аргументах нет вовсе — а значит, нет ни подставного каталога, ни
// схемы одной нотации, подсунутой плечу другой.
func TestRunHasNoPathFlags(t *testing.T) {
	for _, flag := range []string{
		"--writer-schema=" + schemaFile(t, "user_v1.avsc"),
		"--reader-schema=" + schemaFile(t, "user_v1.avsc"),
		"--stand=" + schemasDir(t),
		"--record={\"id\":0}",
	} {
		var buf bytes.Buffer
		args := append(cell("avro", "base", "same"), flag)
		if err := runIn(schemasDir(t), args, &buf); err == nil {
			t.Fatalf("ожидали отказ: аргумента %q не существует", flag)
		}
	}
}

// Плечо и нотация схем — одна координата, а не две. Проверяем не текст
// ошибки, а следствие: каждое плечо действительно работает своими
// схемами, и размеры это показывают.
func TestRunTiesArmToItsOwnNotation(t *testing.T) {
	sizes := map[string]int{}
	for _, format := range []string{"json", "json-schema", "avro", "protobuf"} {
		lines := probeLines(t, cell(format, "base", "same")...)
		if len(lines) != 5 {
			t.Fatalf("%s: получили %d строк, ждали 5", format, len(lines))
		}
		for i, res := range lines {
			if res.Outcome != "ok" {
				t.Fatalf("%s, строка %d: outcome = %q (error=%s)", format, i, res.Outcome, res.Error)
			}
		}
		sizes[format] = lines[0].Bytes
	}
	// json и json-schema обязаны давать ОДНИ И ТЕ ЖЕ байты: разница
	// между ними только в проверке по схеме.
	if sizes["json"] != sizes["json-schema"] {
		t.Fatalf("контроль разошёлся: json = %d, json-schema = %d", sizes["json"], sizes["json-schema"])
	}
	// Двоичные плечи компактнее текстового — если бы плечо ходило
	// чужими схемами, это бы здесь и вылезло.
	if sizes["protobuf"] >= sizes["json"] || sizes["avro"] >= sizes["json"] {
		t.Fatalf("размеры не похожи на свои: %v", sizes)
	}
}

// Записи берутся из стенда, по одной строке на каждую.
func TestRunEmitsOneLinePerCanonicalRecord(t *testing.T) {
	lines := probeLines(t, cell("avro", "base", "same")...)
	if len(lines) != 5 {
		t.Fatalf("получили %d строк, ждали 5", len(lines))
	}
	seen := map[string]bool{}
	for i, res := range lines {
		if res.Lang != "go" {
			t.Fatalf("строка %d: lang = %q", i, res.Lang)
		}
		if res.Format != "avro" || res.Change != "base" || res.Direction != "same" {
			t.Fatalf("строка %d: координаты в строке разошлись со входом: %+v", i, res)
		}
		if res.RecordIndex != i {
			t.Fatalf("строка %d: record_index = %d", i, res.RecordIndex)
		}
		if res.Outcome != "ok" || res.Stage != "decode" || !res.Encoded || res.Bytes == 0 {
			t.Fatalf("строка %d: %+v", i, res)
		}
		if len(res.Record) == 0 || len(res.Want) == 0 {
			t.Fatalf("строка %d: запись и ожидание обязаны быть в строке", i)
		}
		if seen[res.Cell] {
			t.Fatalf("строка %d: ключ %q повторился", i, res.Cell)
		}
		seen[res.Cell] = true
	}
	if lines[0].Record["name"] == lines[1].Record["name"] {
		t.Fatal("две первые строки описывают одну и ту же запись")
	}
}

func TestRunCellKeysDoNotCollideBetweenOps(t *testing.T) {
	compat := probeLines(t, append(cell("protobuf", "unknown_field", "newer_writer"), "--op=compat")...)
	round := probeLines(t, append(cell("protobuf", "unknown_field", "newer_writer"), "--op=roundtrip")...)
	keys := map[string]bool{}
	for _, res := range append(append([]probeResult{}, compat...), round...) {
		if keys[res.Cell] {
			t.Fatalf("ключ %q повторился между --op=compat и --op=roundtrip", res.Cell)
		}
		keys[res.Cell] = true
	}
	if len(keys) != 10 {
		t.Fatalf("получили %d различных ключей, ждали 10", len(keys))
	}
}

func TestRunRejectsIncompleteCoordinates(t *testing.T) {
	cases := [][]string{
		{"--change=base", "--direction=same"},            // нет плеча
		{"--format=avro", "--direction=same"},            // нет изменения
		{"--format=avro", "--change=base"},               // нет направления
		cell("xml", "base", "same"),                      // неизвестное плечо
		cell("avro", "нет-такого", "newer_reader"),       // неизвестное изменение
		cell("avro", "remove", "вбок"),                   // неизвестное направление
		cell("avro", "base", "newer_reader"),             // у base нет второй версии
		append(cell("avro", "base", "same"), "--op=zzz"), // неизвестный вид пробы
	}
	for _, args := range cases {
		var buf bytes.Buffer
		if err := runIn(schemasDir(t), args, &buf); err == nil {
			t.Fatalf("%v: ожидали отказ", args)
		} else if buf.Len() != 0 {
			t.Fatalf("%v: на отказе вывода быть не должно: %s", args, buf.String())
		}
	}
}

// Тихая порча protobuf при смене типа поля: ни в одной из пяти записей
// значение не совпадает с нулевым значением типа.
func TestRunCatchesRetypeCorruptionOnEveryRecord(t *testing.T) {
	lines := probeLines(t, cell("protobuf", "retype", "newer_reader")...)
	if len(lines) != 5 {
		t.Fatalf("получили %d строк, ждали 5", len(lines))
	}
	for i, res := range lines {
		if res.Want["id"] == "" || res.Want["id"] == nil {
			t.Fatalf("строка %d: want.id = %#v — ожидание не должно совпадать с нулевым значением типа", i, res.Want["id"])
		}
		if res.Outcome != "wrong" {
			t.Fatalf("строка %d: outcome = %q, ждали wrong (error=%s)", i, res.Outcome, res.Error)
		}
	}
}

func TestRunHandlesNewerWriterDefaults(t *testing.T) {
	for i, res := range probeLines(t, cell("protobuf", "remove", "newer_writer")...) {
		if res.Outcome != "ok" {
			t.Fatalf("строка %d: outcome = %q, ждали ok (error=%s)", i, res.Outcome, res.Error)
		}
	}
}

// Вырожденная пара схем — n/a на каждой записи; кодек не запускается, и
// строка это показывает: ни признака кодирования, ни стадии, ни записи,
// ни ожидания.
func TestRunReportsNAForDegenerateSchemaPair(t *testing.T) {
	lines := probeLines(t, cell("avro", "reuse_tag", "newer_reader")...)
	if len(lines) != 5 {
		t.Fatalf("получили %d строк, ждали 5", len(lines))
	}
	for i, res := range lines {
		if res.Outcome != "n/a" {
			t.Fatalf("строка %d: outcome = %q, ждали n/a", i, res.Outcome)
		}
		if res.Encoded || res.Bytes != 0 || res.Stage != "" {
			t.Fatalf("строка %d: кодек не должен был запускаться: %+v", i, res)
		}
		if len(res.Record) != 0 || len(res.Want) != 0 {
			t.Fatalf("строка %d: у n/a нет ни записи, ни ожидания", i)
		}
	}
}

func TestRunOpRoundtripSurvivesUnknownField(t *testing.T) {
	for i, res := range probeLines(t, append(cell("protobuf", "unknown_field", "newer_writer"), "--op=roundtrip")...) {
		if res.Kind != "roundtrip" || res.Stage != "decode-final" || res.Outcome != "ok" {
			t.Fatalf("строка %d: %+v (error=%s)", i, res, res.Error)
		}
		if !res.Encoded {
			t.Fatalf("строка %d: encoded=false при пройденном круге", i)
		}
	}
}

func TestRunOpRoundtripGivesNAForFormatWithoutUnknownCarrier(t *testing.T) {
	lines := probeLines(t, append(cell("avro", "base", "same"), "--op=roundtrip")...)
	if len(lines) != 5 {
		t.Fatalf("получили %d строк, ждали 5", len(lines))
	}
	for i, res := range lines {
		if res.Outcome != "n/a" {
			t.Fatalf("строка %d: outcome = %q, ждали n/a", i, res.Outcome)
		}
	}
}

func TestRunOpRoundtripGivesNAWhenReaderDoesNotCleanlyDropAField(t *testing.T) {
	for i, res := range probeLines(t, append(cell("protobuf", "retype", "newer_reader"), "--op=roundtrip")...) {
		if res.Outcome != "n/a" {
			t.Fatalf("строка %d: outcome = %q, ждали n/a (retype — не про сохранение неизвестного поля)", i, res.Outcome)
		}
		// Круг правок 7: ровно эти строки и различали два потока — Go
		// печатал у них запись и ожидание вопреки собственной спеке.
		if len(res.Record) != 0 || len(res.Want) != 0 {
			t.Fatalf("строка %d: у n/a нет ни записи, ни ожидания", i)
		}
		if res.Stage != "" || res.Encoded {
			t.Fatalf("строка %d: %+v", i, res)
		}
	}
}

func TestRunMarksWhetherEncodingActuallyHappened(t *testing.T) {
	for i, res := range probeLines(t, cell("protobuf", "remove", "newer_reader")...) {
		if !res.Encoded {
			t.Fatalf("строка %d: encoded=false, хотя запись закодирована (bytes=%d)", i, res.Bytes)
		}
	}
	for i, res := range probeLines(t, cell("json-schema", "reuse_tag", "newer_reader")...) {
		if res.Outcome != "n/a" || res.Encoded {
			t.Fatalf("строка %d: %+v", i, res)
		}
	}
}

// --- ось размера: --op=size ---------------------------------------------

// sizeLines гоняет пробу в виде --op=size и разбирает строки в
// sizeResult — отдельную форму строки, у которой нет полей compat/
// roundtrip (outcome, stage, want), зато есть zstd и schema_bytes.
func sizeLines(t *testing.T, format, change, direction string) []sizeResult {
	t.Helper()
	var buf bytes.Buffer
	args := append(cell(format, change, direction), "--op=size")
	if err := runIn(schemasDir(t), args, &buf); err != nil {
		t.Fatalf("runIn(%v): %v", args, err)
	}
	var out []sizeResult
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var res sizeResult
		if err := json.Unmarshal([]byte(line), &res); err != nil {
			t.Fatalf("строка вывода — не валидный JSON: %v\nстрока: %s", err, line)
		}
		out = append(out, res)
	}
	return out
}

// Контроль и json-schema обязаны дать РОВНО ОДИНАКОВЫЙ bytes — байты те
// же самые (§7.1 spec.md), и это заявление будущей статьи проверяется
// здесь, а не предполагается. Вес схемы у контроля обязан быть нулём в
// ОБОИХ измерениях — канонической форме и файле как есть: он
// содержательный, а не пропуск.
func TestRunOpSizeControlArmMatchesJSONSchemaAndHasNoSchemaWeight(t *testing.T) {
	control := sizeLines(t, "json", "base", "same")
	schema := sizeLines(t, "json-schema", "base", "same")
	if len(control) != 5 || len(schema) != 5 {
		t.Fatalf("получили %d/%d строк, ждали 5/5", len(control), len(schema))
	}
	for i := range control {
		if control[i].Bytes != schema[i].Bytes {
			t.Fatalf("запись %d: json.bytes=%d, json-schema.bytes=%d — контроль обязан совпадать побайтово",
				i, control[i].Bytes, schema[i].Bytes)
		}
		if control[i].SchemaBytes != 0 || control[i].SchemaFileBytes != 0 {
			t.Fatalf("запись %d: у контроля schema_bytes=%d, schema_file_bytes=%d, ждали 0/0 — читателю схема не нужна вовсе",
				i, control[i].SchemaBytes, control[i].SchemaFileBytes)
		}
	}
}

// У avro/protobuf/json-schema вес схемы — вес файла схемы, а не ноль:
// читателю без него нечем декодировать байты. Круг ревью 2, находка C2:
// каноническая форма (SchemaBytes) не может весить БОЛЬШЕ файла как есть
// (SchemaFileBytes) — она снимает форматирование, а не добавляет его; у
// Protobuf они РАВНЫ (дескриптор уже канонический — без source_code_info,
// круг ревью 2, находка C1), у Avro/JSON Schema каноническая форма
// обязана быть строго легче: у неё нет отступов, которые есть у файла.
func TestRunOpSizeCanonicalWeightNeverExceedsFileWeight(t *testing.T) {
	for _, tc := range []struct {
		format string
		strict bool // канонический вес обязан быть СТРОГО меньше файла
	}{
		{"json-schema", true},
		{"avro", true},
		{"protobuf", false}, // дескриптор уже канонический — веса равны
	} {
		lines := sizeLines(t, tc.format, "base", "same")
		if len(lines) != 5 {
			t.Fatalf("%s: получили %d строк, ждали 5", tc.format, len(lines))
		}
		for i, res := range lines {
			if res.SchemaBytes <= 0 || res.SchemaFileBytes <= 0 {
				t.Fatalf("%s, запись %d: schema_bytes=%d, schema_file_bytes=%d, ждали >0/>0",
					tc.format, i, res.SchemaBytes, res.SchemaFileBytes)
			}
			if res.Bytes <= 0 {
				t.Fatalf("%s, запись %d: bytes=%d, ждали > 0", tc.format, i, res.Bytes)
			}
			if res.Zstd <= 0 {
				t.Fatalf("%s, запись %d: zstd=%d, ждали > 0", tc.format, i, res.Zstd)
			}
			if tc.strict && res.SchemaBytes >= res.SchemaFileBytes {
				t.Fatalf("%s, запись %d: schema_bytes=%d не меньше schema_file_bytes=%d — каноническая форма обязана быть легче размеченного отступами файла",
					tc.format, i, res.SchemaBytes, res.SchemaFileBytes)
			}
			if !tc.strict && res.SchemaBytes != res.SchemaFileBytes {
				t.Fatalf("%s, запись %d: schema_bytes=%d != schema_file_bytes=%d — дескриптор уже канонический, они обязаны совпасть",
					tc.format, i, res.SchemaBytes, res.SchemaFileBytes)
			}
		}
	}
}

// Каноническая форма Avro — не наша выдумка: это ровно то число, что
// в §10.3 spec.md зафиксировано измерением (Parsing Canonical Form по
// спецификации Avro). Регрессионный якорь на КОНКРЕТНОЕ число, а не
// только на "меньше файла": если библиотека когда-нибудь поменяет
// формулу PCF, тест покраснеет прежде, чем это попадёт в статью molча.
func TestRunOpSizeAvroCanonicalWeightMatchesKnownValue(t *testing.T) {
	lines := sizeLines(t, "avro", "base", "same")
	const wantCanonical = 162
	const wantFile = 221
	for i, res := range lines {
		if res.SchemaBytes != wantCanonical || res.SchemaFileBytes != wantFile {
			t.Fatalf("запись %d: schema_bytes=%d, schema_file_bytes=%d, ждали %d/%d",
				i, res.SchemaBytes, res.SchemaFileBytes, wantCanonical, wantFile)
		}
	}
}

// Круг ревью 2, находка C1: дескриптор Protobuf собран БЕЗ
// source_code_info — якорь на конкретное число, чтобы регрессия сборки
// (кто-то убрал --exclude-source-info) не прошла молча.
func TestRunOpSizeProtobufDescriptorHasNoSourceInfoOverhead(t *testing.T) {
	lines := sizeLines(t, "protobuf", "base", "same")
	const want = 119
	for i, res := range lines {
		if res.SchemaBytes != want {
			t.Fatalf("запись %d: schema_bytes=%d, ждали %d — похоже, source_code_info вернулся в дескриптор",
				i, res.SchemaBytes, want)
		}
	}
}

// Круг ревью 2, находка C3 + C4: закоммиченное число сжатой пачки
// protobuf не воспроизводилось (восемь прогонов дали разброс 141-148,
// а закоммиченное 136 не совпало НИ С ОДНИМ) — из-за недетерминированного
// порядка полей у *dynamicpb.Message. Прежняя проверка детерминизма
// сравнивала сжатую ОДНУ запись, которая расходиться не может по
// построению (длина и порядок байт внутри одного значения не зависят от
// порядка обхода ПОЛЕЙ СООБЩЕНИЯ — там как раз один скаляр на поле).
// Плывёт только ПАЧКА: несколько полей сообщения сериализуются в одном
// потоке, и порядок между ними как раз и был недетерминирован.
//
// Проверяем именно то, что плыло: batch_hash (содержимое, а не длину —
// длина не изменилась бы даже при старой порче, см. находку M1) на
// восьми прогонах подряд.
func TestRunOpSizeProtobufBatchHashIsDeterministicAcrossEightRuns(t *testing.T) {
	var hashes []string
	var zstds []int
	for i := 0; i < 8; i++ {
		lines := sizeLines(t, "protobuf", "base", "same")
		hashes = append(hashes, lines[0].BatchHash)
		zstds = append(zstds, lines[0].BatchZstd)
	}
	for i := 1; i < len(hashes); i++ {
		if hashes[i] != hashes[0] {
			t.Fatalf("прогон %d: batch_hash разошёлся: %s vs %s (все хеши: %v)", i, hashes[i], hashes[0], hashes)
		}
		if zstds[i] != zstds[0] {
			t.Fatalf("прогон %d: batch_zstd разошёлся: %d vs %d (все значения: %v)", i, zstds[i], zstds[0], zstds)
		}
	}
}

// Круг ревью 3: batch_content_hash — содержимое пачки ПОСЛЕ расшифровки,
// а не байты с провода. У ОДНОЙ реализации на фиксированных данных оно
// обязано быть стабильным на восьми прогонах подряд — ровно как
// batch_hash, но по другой причине: расшифровка детерминирована всегда
// (JSON/JSON Schema просто не имеют недетерминированной СБОРКИ, в
// отличие от protobuf, — вопрос там не в кодировании, а в порядке
// ключей объекта, см. следующий тест).
func TestRunOpSizeBatchContentHashIsDeterministicAcrossEightRuns(t *testing.T) {
	for _, format := range []string{"json", "json-schema", "avro", "protobuf"} {
		var hashes []string
		for i := 0; i < 8; i++ {
			lines := sizeLines(t, format, "base", "same")
			hashes = append(hashes, lines[0].BatchContentHash)
		}
		for i := 1; i < len(hashes); i++ {
			if hashes[i] != hashes[0] {
				t.Fatalf("%s, прогон %d: batch_content_hash разошёлся: %s vs %s",
					format, i, hashes[i], hashes[0])
			}
		}
		if hashes[0] == "" {
			t.Fatalf("%s: batch_content_hash пуст", format)
		}
	}
}

// Круг ревью 3, главная проверка: batch_content_hash — единственное
// поле, которое ОБЯЗАНО совпасть между ЛЮБЫМИ форматами на одних и тех
// же канонических данных — оно хеширует расшифрованное содержимое
// (те же пять записей records.json), а не байты кодека, а значит формат
// кодирования на него не влияет вовсе. batch_hash так не может: у него
// разные байты на проводе для разных форматов по построению.
func TestRunOpSizeBatchContentHashIsTheSameAcrossAllFourFormats(t *testing.T) {
	var first string
	for i, format := range []string{"json", "json-schema", "avro", "protobuf"} {
		lines := sizeLines(t, format, "base", "same")
		if i == 0 {
			first = lines[0].BatchContentHash
			continue
		}
		if lines[0].BatchContentHash != first {
			t.Fatalf("%s: batch_content_hash = %s, ждали %s (как у json) — расшифрованное содержимое обязано совпасть независимо от формата",
				format, lines[0].BatchContentHash, first)
		}
	}
}

// batch_hash и batch_content_hash отвечают на разные вопросы: у json
// АКТИВНО плечо не имеет канонической формы байт (порядок ключей JSON
// не специфицирован), поэтому опираться в статье можно только на
// batch_content_hash. Тест документирует, что оба поля присутствуют и
// не совпадают друг с другом (иначе одно из полей было бы избыточным
// ровно на этом плече).
func TestRunOpSizeBatchHashAndContentHashAreDifferentFieldsForJSON(t *testing.T) {
	lines := sizeLines(t, "json", "base", "same")
	if lines[0].BatchHash == "" || lines[0].BatchContentHash == "" {
		t.Fatalf("оба поля обязаны быть непустыми: %+v", lines[0])
	}
	if lines[0].BatchHash == lines[0].BatchContentHash {
		t.Fatalf("batch_hash и batch_content_hash случайно совпали — тест ничего не показывает, замени данные")
	}
}

// batch_bytes/batch_zstd — свойства КЛЕТКИ, а не отдельной записи:
// печатаются одним и тем же значением на всех пяти строках (spec.md
// §10.3.2), тем же принципом, каким клеточные исходы compat печатаются
// на каждой записи.
func TestRunOpSizeBatchIsSameAcrossAllRecordsOfTheCell(t *testing.T) {
	lines := sizeLines(t, "avro", "base", "same")
	if len(lines) != 5 {
		t.Fatalf("получили %d строк, ждали 5", len(lines))
	}
	first := lines[0]
	if first.BatchBytes <= 0 || first.BatchZstd <= 0 {
		t.Fatalf("batch_bytes/batch_zstd обязаны быть больше нуля: %+v", first)
	}
	for i, res := range lines {
		if res.BatchBytes != first.BatchBytes || res.BatchZstd != first.BatchZstd {
			t.Fatalf("строка %d: batch разошёлся внутри клетки: %+v vs %+v", i, res, first)
		}
	}
}

// Пачка обязана весить БОЛЬШЕ пяти обрамлений (4 байта длины на каждую
// запись) плюс суммы bytes всех пяти записей — обрамление добавляет
// байты, а не вычитает и не теряется при склейке.
func TestRunOpSizeBatchBytesAccountsForFraming(t *testing.T) {
	lines := sizeLines(t, "protobuf", "base", "same")
	sum := 0
	for _, res := range lines {
		sum += res.Bytes
	}
	want := sum + 4*len(lines)
	if lines[0].BatchBytes != want {
		t.Fatalf("batch_bytes = %d, ждали %d (сумма bytes + 4 байта обрамления на запись)",
			lines[0].BatchBytes, want)
	}
}

// Пачка меряет сжатие на входе побольше одной записи: на пяти записях
// разница между форматами не обязана тонуть в оверхеде заголовка кадра
// так, как это происходит на одной записи (Zstd там больше Bytes).
// Проверяем следствие: сжатая пачка компактнее суммы пяти отдельно
// сжатых записей — экономия на разделяемом словаре/заголовке появляется
// именно потому, что сжимали ВМЕСТЕ, а не каждую отдельно.
func TestRunOpSizeBatchZstdIsSmallerThanSumOfPerRecordZstd(t *testing.T) {
	lines := sizeLines(t, "avro", "base", "same")
	sumZstd := 0
	for _, res := range lines {
		sumZstd += res.Zstd
	}
	if lines[0].BatchZstd >= sumZstd {
		t.Fatalf("batch_zstd=%d не меньше суммы поштучных zstd=%d — пачка не даёт ожидаемой экономии",
			lines[0].BatchZstd, sumZstd)
	}
}

// --op=size осмысленно только для одной схемы: направление обязано
// быть same. newer_reader/newer_writer описывают ПАРУ схем, а размер —
// свойство одной.
func TestRunOpSizeRequiresSameDirection(t *testing.T) {
	for _, direction := range []string{"newer_reader", "newer_writer"} {
		var buf bytes.Buffer
		args := append(cell("avro", "add_default", direction), "--op=size")
		if err := runIn(schemasDir(t), args, &buf); err == nil {
			t.Fatalf("направление %s: ожидали отказ — размер меряется на одной схеме", direction)
		} else if buf.Len() != 0 {
			t.Fatalf("направление %s: вывода быть не должно: %s", direction, buf.String())
		}
	}
}

// --- проверки на временном стенде -------------------------------------

// Неполная запись обязана быть жёстким отказом ОДИНАКОВО в обоих
// режимах: в круговом прогоне такая же поломка когда-то давала строку
// «refused» с нулевым кодом возврата, то есть попадала в таблицу как
// поведение формата.
func TestRunRejectsIncompleteRecordInBothOps(t *testing.T) {
	dir := tempStand(t,
		`{"v1":{"records":[{"name":"Анна","email":"a@b"}]},
		  "v2":{"unknown_field":{"records":[{"id":1,"name":"Анна","email":"a@b","nickname":"anya"}]}}}`,
		"user_v1.desc", "user_v2_unknown_field.desc")
	for _, op := range []string{"compat", "roundtrip"} {
		var buf bytes.Buffer
		args := append(cell("protobuf", "unknown_field", "newer_reader"), "--op="+op)
		if err := runIn(dir, args, &buf); err == nil {
			t.Fatalf("--op=%s: ожидали жёсткий отказ — запись не покрывает поля схемы писателя", op)
		} else if buf.Len() != 0 {
			t.Fatalf("--op=%s: вывода быть не должно: %s", op, buf.String())
		}
	}
}

func TestRunRejectsRecordWithWrongFieldTypeInBothOps(t *testing.T) {
	dir := tempStand(t,
		`{"v1":{"records":[{"id":"1","name":"Анна","email":"a@b"}]},
		  "v2":{"unknown_field":{"records":[{"id":1,"name":"Анна","email":"a@b","nickname":"anya"}]}}}`,
		"user_v1.desc", "user_v2_unknown_field.desc")
	for _, op := range []string{"compat", "roundtrip"} {
		var buf bytes.Buffer
		args := append(cell("protobuf", "unknown_field", "newer_reader"), "--op="+op)
		if err := runIn(dir, args, &buf); err == nil {
			t.Fatalf("--op=%s: ожидали жёсткий отказ — тип id не совпадает со схемой писателя", op)
		}
	}
}

// Клетка, где схема писателя и читателя ОДНА И ТА ЖЕ, не имеет права
// показывать «прочиталось, но не то» из-за нашего усечения: значение,
// не помещающееся в объявленный схемой int32, — отказ до кодирования.
func TestRunRejectsValueTooWideForDeclaredFieldOnSameSchema(t *testing.T) {
	dir := tempStand(t,
		`{"v1":{"records":[{"id":1,"name":"Анна","email":"a@b"}]},
		  "v2":{"reuse_tag":{"records":[{"id":1,"name":"Анна","login_count":3000000000}]}}}`,
		"user_v1.desc", "user_v2_reuse_tag.desc")
	var buf bytes.Buffer
	if err := runIn(dir, cell("protobuf", "reuse_tag", "same"), &buf); err == nil {
		t.Fatal("ожидали отказ: 3000000000 не помещается в объявленный схемой int32")
	} else if buf.Len() != 0 {
		t.Fatalf("вывода быть не должно: %s", buf.String())
	}
}

func TestRunRejectsMissingRecordsFile(t *testing.T) {
	dir := tempStand(t, `{"v1":{"records":[{"id":1,"name":"Анна","email":"a@b"}]},"v2":{}}`, "user_v1.avsc")
	if err := os.Remove(filepath.Join(dir, "records.json")); err != nil {
		t.Fatalf("удаление записей: %v", err)
	}
	var buf bytes.Buffer
	if err := runIn(dir, cell("avro", "base", "same"), &buf); err == nil {
		t.Fatal("ожидали ошибку: в стенде нет records.json")
	}
}

// Стенд без манифеста — не стенд.
func TestRunRejectsStandWithoutManifest(t *testing.T) {
	dir := tempStand(t, `{"v1":{"records":[{"id":1,"name":"Анна","email":"a@b"}]},"v2":{}}`, "user_v1.avsc")
	if err := os.Remove(filepath.Join(dir, stand.ManifestFileName)); err != nil {
		t.Fatalf("удаление манифеста: %v", err)
	}
	var buf bytes.Buffer
	if err := runIn(dir, cell("avro", "base", "same"), &buf); err == nil {
		t.Fatal("ожидали ошибку: у каталога нет манифеста")
	} else if buf.Len() != 0 {
		t.Fatalf("вывода быть не должно: %s", buf.String())
	}
}

// Подмена файла внутри стенда: манифест не тронут, содержимое чужое.
func TestRunRefusesRenamedCopyOfAnotherSchema(t *testing.T) {
	dir := tempStand(t, `{"v1":{"records":[{"id":1,"name":"Анна","email":"a@b"}]},
	  "v2":{"retype":{"records":[{"id":"1","name":"Анна","email":"a@b"}]}}}`,
		"user_v1.desc", "user_v2_retype.desc")
	raw, err := os.ReadFile(schemaFile(t, "user_v2_add_default.desc"))
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "user_v2_retype.desc"), raw, 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}
	var buf bytes.Buffer
	if err := runIn(dir, cell("protobuf", "retype", "newer_reader"), &buf); err == nil {
		t.Fatal("ожидали отказ: содержимое схемы не совпадает с манифестом")
	} else if buf.Len() != 0 {
		t.Fatalf("правдоподобная строка на подменённых данных: %s", buf.String())
	}
}

// Сбой САМОЙ ПРОБЫ (схему не удалось скомпилировать) обязан давать
// отдельный исход, а не «формат отказал».
func TestRunReportsProbeFailureSeparatelyFromRefusal(t *testing.T) {
	broken := `{"type":"object",
	  "properties":{"id":{"$ref":"#/definitions/нет-такого"},"name":{"type":"string"},"email":{"type":"string"}},
	  "required":["id","name","email"],"additionalProperties":false}`
	dir := buildStand(t,
		`{"v1":{"records":[{"id":1,"name":"Анна","email":"a@b"}]},"v2":{}}`,
		nil, map[string]string{"user_v1.json": broken})
	for i, res := range linesIn(t, dir, cell("json-schema", "base", "same")...) {
		if res.Outcome != "error" {
			t.Fatalf("строка %d: outcome = %q, ждали error — это сбой пробы, а не отказ формата (error=%s)",
				i, res.Outcome, res.Error)
		}
		// Круг правок 7: сбой пробы — исход КЛЕТКИ, а не случайной
		// стадии. Схемы приводятся в рабочий вид до первой записи,
		// поэтому у строк error пустая стадия и нет ни записи, ни
		// ожидания: наличие ожидания в строке означает, что оно
		// вычислялось.
		if res.Stage != "" || res.Encoded || res.Bytes != 0 {
			t.Fatalf("строка %d: %+v", i, res)
		}
		if len(res.Record) != 0 || len(res.Want) != 0 {
			t.Fatalf("строка %d: у error нет ни записи, ни ожидания", i)
		}
	}
}

// Отказ на стадии ENCODE обязан быть виден по stage. Схема ниже
// объявляет ограничение, которому каноническая по форме запись не
// удовлетворяет: поля и типы те же, а значение не проходит проверку по
// схеме ПИСАТЕЛЯ.
func TestRunReportsEncodeStageOnEncodeFailure(t *testing.T) {
	strict := `{"type":"object",
	  "properties":{"id":{"type":"integer"},"name":{"type":"string","maxLength":2},"email":{"type":"string"}},
	  "required":["id","name","email"],"additionalProperties":false}`
	dir := buildStand(t,
		`{"v1":{"records":[{"id":1,"name":"Анна","email":"a@b"}]},"v2":{}}`,
		nil, map[string]string{"user_v1.json": strict})
	for i, res := range linesIn(t, dir, cell("json-schema", "base", "same")...) {
		if res.Stage != "encode" || res.Outcome != "refused" || res.Encoded {
			t.Fatalf("строка %d: %+v", i, res)
		}
		if len(res.Record) == 0 || len(res.Want) == 0 {
			t.Fatalf("строка %d: у отказа формата запись и ожидание обязаны быть — они вычислялись", i)
		}
	}
}

// Круг правок 7, пункт 2: лишний ключ в записи — жёсткий отказ в обоих
// режимах. Клетка ниже — та самая, где схема писателя и читателя ОДНА
// И ТА ЖЕ: без проверки контрольное плечо давало бы там «прочиталось,
// но не то», а json-schema — отказ формата.
func TestRunRejectsRecordWithKeyAbsentFromWriterSchema(t *testing.T) {
	dir := tempStand(t,
		`{"v1":{"records":[{"id":1,"name":"Анна","email":"a@b","лишнее":"поле"}]},"v2":{}}`,
		"user_v1.json", "user_v1.avsc", "user_v1.desc")
	// Круговая проба у трёх плеч из четырёх не доходит до записей
	// вовсе: «у плеча нет непрозрачного остатка» — ответ по плечу, без
	// схем и без данных (приоритет причин, см. spec.md). Поэтому там
	// лишний ключ и не может быть замечен — и это правильно.
	type run struct{ format, op string }
	for _, r := range []run{
		{"json", "compat"}, {"json-schema", "compat"},
		{"avro", "compat"}, {"protobuf", "compat"},
		{"protobuf", "roundtrip"},
	} {
		var buf bytes.Buffer
		args := append(cell(r.format, "base", "same"), "--op="+r.op)
		if err := runIn(dir, args, &buf); err == nil {
			t.Fatalf("%s/--op=%s: ожидали жёсткий отказ на лишнем ключе", r.format, r.op)
		} else if buf.Len() != 0 {
			t.Fatalf("%s/--op=%s: вывода быть не должно: %s", r.format, r.op, buf.String())
		}
	}
}

// Круг правок 7, пункт 1: решение «совпадение номера — не тождество»
// теперь видно В ИСХОДЕ, а не только в печатном ожидании. Каноническая
// запись 3 несёт email "0"; если приводить тип на переиспользованном
// номере, ожидание станет целым 0 — ровно тем, что возвращает чтение, —
// и строка перевернётся в ok.
func TestReusedFieldNumberDecisionIsVisibleInOutcome(t *testing.T) {
	lines := probeLines(t, cell("protobuf", "reuse_tag", "newer_reader")...)
	if len(lines) != 5 {
		t.Fatalf("получили %d строк, ждали 5", len(lines))
	}
	for i, res := range lines {
		if res.Outcome != "wrong" {
			t.Fatalf("строка %d: outcome = %q, ждали wrong", i, res.Outcome)
		}
	}
	// Именно эта строка и делает решение проверяемым таблицей.
	if lines[3].Record["email"] != "0" {
		t.Fatalf("запись 3: email = %#v, ждали \"0\" — на ней держится проверка решения о слоте",
			lines[3].Record["email"])
	}
	if lines[3].Want["login_count"] != "0" {
		t.Fatalf("запись 3: want.login_count = %#v, ждали строку \"0\"", lines[3].Want["login_count"])
	}
}

// НАБЛЮДЕНИЕ, КОТОРОЕ СТЕНД ОБЯЗАН СОХРАНЯТЬ, А НЕ ПРЯТАТЬ.
//
// Тихая порча у protobuf невидима ровно тогда, когда истинное значение
// совпадает с нулевым значением типа читателя, и только на пути
// приведения строка → целое. Писатель объявляет id строкой и пишет
// "0"; читатель ждёт целое, роняет поле по несовпадению типа провода и
// видит 0 — то самое, что честное ожидание и предсказывает.
//
// Это свойство ФОРМАТА и материал для статьи, а не дефект вычисления
// ожидания. Вывод для стенда другой: устойчивость клетки зависит от
// ДАННЫХ, и одна запись на клетку этого не покажет — покажет прогон по
// всем, где строки клетки разойдутся между собой.
func TestProtobufSilentCorruptionIsInvisibleAtTypeZeroValue(t *testing.T) {
	dir := tempStand(t,
		`{"v1":{"records":[{"id":1,"name":"Один","email":"o@example.com"}]},
		  "v2":{"retype":{"records":[
		    {"id":"0","name":"Ноль","email":"z@example.com"},
		    {"id":"1","name":"Один","email":"o@example.com"}]}}}`,
		"user_v1.desc", "user_v2_retype.desc")

	lines := linesIn(t, dir, cell("protobuf", "retype", "newer_writer")...)
	if len(lines) != 2 {
		t.Fatalf("получили %d строк, ждали 2", len(lines))
	}
	if lines[0].Outcome != "ok" {
		t.Fatalf("запись с id=\"0\": outcome = %q, ждали ok — порча совпала с нулевым значением типа", lines[0].Outcome)
	}
	if lines[1].Outcome != "wrong" {
		t.Fatalf("запись с id=\"1\": outcome = %q, ждали wrong", lines[1].Outcome)
	}
	if lines[0].Outcome == lines[1].Outcome {
		t.Fatal("две записи одной клетки дали один исход — расхождение, ради которого гоняются все записи, потерялось")
	}
}

// В обратную сторону невидимость не работает даже при нуле: целое 0
// приводится к строке "0", а чтение даёт пустую строку.
func TestSilentCorruptionIsVisibleInTheOppositeDirectionEvenAtZero(t *testing.T) {
	dir := tempStand(t,
		`{"v1":{"records":[{"id":0,"name":"Ноль","email":"z@example.com"}]},
		  "v2":{"retype":{"records":[{"id":"0","name":"Ноль","email":"z@example.com"}]}}}`,
		"user_v1.desc", "user_v2_retype.desc")
	for i, res := range linesIn(t, dir, cell("protobuf", "retype", "newer_reader")...) {
		if res.Outcome != "wrong" {
			t.Fatalf("строка %d: outcome = %q, ждали wrong: 0 -> \"0\", а чтение даёт пустую строку", i, res.Outcome)
		}
	}
}

// --- сборка временного стенда -----------------------------------------

// tempStand собирает временный стенд: копии нужных файлов схем, свой
// records.json и СВОЙ МАНИФЕСТ. Нужен там, где проверяется реакция на
// испорченные данные стенда — портить настоящий каталог нельзя.
//
// Манифест здесь настоящий, а не поддельный: временный стенд — это
// отдельный стенд со своим корнем доверия, а не обход манифеста
// основного. Проверки того, что подмена файла останавливает пробу,
// манифест как раз не трогают.
func tempStand(t *testing.T, records string, schemaNames ...string) string {
	t.Helper()
	return buildStand(t, records, schemaNames, nil)
}

// buildStand: copied — файлы, скопированные из настоящего стенда;
// custom — файлы, написанные самим тестом (имя -> содержимое).
func buildStand(t *testing.T, records string, copied []string, custom map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]any{}

	for _, name := range copied {
		raw, err := os.ReadFile(schemaFile(t, name))
		if err != nil {
			t.Fatalf("чтение %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
			t.Fatalf("запись %s: %v", name, err)
		}
		files[name] = manifestEntry(t, dir, name)
	}
	for name, content := range custom {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("запись %s: %v", name, err)
		}
		files[name] = manifestEntry(t, dir, name)
	}
	if err := os.WriteFile(filepath.Join(dir, "records.json"), []byte(records), 0o644); err != nil {
		t.Fatalf("запись records.json: %v", err)
	}
	digest, err := stand.FileDigest(filepath.Join(dir, "records.json"), false)
	if err != nil {
		t.Fatalf("дайджест записей: %v", err)
	}
	files["records.json"] = map[string]any{"digest": digest, "role": "records", "content": "text"}

	raw, err := json.Marshal(map[string]any{"algorithm": "sha256", "files": files})
	if err != nil {
		t.Fatalf("сборка манифеста: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, stand.ManifestFileName), raw, 0o644); err != nil {
		t.Fatalf("запись манифеста: %v", err)
	}
	return dir
}

var schemaNameRe = regexp.MustCompile(`^user_v(\d+)(?:_(.+))?\.(\w+)$`)

var notations = map[string]string{"avsc": "avro", "desc": "protobuf", "json": "json-schema"}

func manifestEntry(t *testing.T, dir, name string) map[string]any {
	t.Helper()
	m := schemaNameRe.FindStringSubmatch(name)
	if m == nil {
		t.Fatalf("имя %q не разбирается на версию и изменение", name)
	}
	version, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("версия в имени %q: %v", name, err)
	}
	binary := m[3] == "desc"
	digest, err := stand.FileDigest(filepath.Join(dir, name), binary)
	if err != nil {
		t.Fatalf("дайджест %s: %v", name, err)
	}
	content := "text"
	if binary {
		content = "binary"
	}
	return map[string]any{
		"digest": digest, "role": "schema", "content": content,
		"notation": notations[m[3]], "version": version, "change": m[2],
	}
}

// --- Задача 8: ось перекрёстного чтения --------------------------------

// crossExchangeDir создаёт временный рабочий каталог для файлов обмена и
// переключает в него процесс: exchange.WriteCross/ReadCross используют
// ТЕКУЩИЙ РАБОЧИЙ КАТАЛОГ, а не аргумент (spec.md §17.2) — переключение
// именно так и предполагается вызывающим сценарием (bench/run-cross.sh
// делает `cd` в каталог обмена перед запуском пробы).
func crossExchangeDir(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
}

func TestRunCrossAcceptRequiresWriterLang(t *testing.T) {
	crossExchangeDir(t)
	for _, bad := range []string{"", "python", "GO"} {
		var buf bytes.Buffer
		args := append(cell("avro", "rename", "newer_reader"), "--op=cross-accept")
		if bad != "" {
			args = append(args, "--writer-lang="+bad)
		}
		if err := runIn(schemasDir(t), args, &buf); err == nil {
			t.Fatalf("--writer-lang=%q: ожидали отказ", bad)
		}
	}
}

func TestRunWriterLangOnlyMeaningfulForCrossAccept(t *testing.T) {
	crossExchangeDir(t)
	for _, op := range []string{"compat", "roundtrip", "size", "cross-emit", "identity"} {
		var buf bytes.Buffer
		direction := "newer_reader"
		if op == "size" || op == "identity" {
			direction = "same"
		}
		args := append(cell("avro", "rename", direction), "--op="+op, "--writer-lang=go")
		if err := runIn(schemasDir(t), args, &buf); err == nil {
			t.Fatalf("--op=%s: --writer-lang не имеет смысла тут — ожидали отказ", op)
		}
	}
}

// TestRunCrossEmitThenAcceptMatchesCompatControl — «своя же контрольная
// клетка»: писатель и читатель — один и тот же процесс (тут это
// единственная реализация, доступная тесту, — Go), поэтому обмен через
// файл ОБЯЗАН дать построчно то же самое, что и обычная compat-проба на
// тех же координатах (решение контроллера Задачи 8: расхождение здесь —
// Critical, а не «другой путь кода»).
func TestRunCrossEmitThenAcceptMatchesCompatControl(t *testing.T) {
	crossExchangeDir(t)
	dir := schemasDir(t)

	for _, coord := range []struct{ format, change, direction string }{
		{"avro", "add_default", "newer_reader"},
		{"avro", "alias_conflict", "newer_reader"},
		{"protobuf", "unknown_field", "newer_writer"},
	} {
		var emitBuf bytes.Buffer
		emitArgs := append(cell(coord.format, coord.change, coord.direction), "--op=cross-emit")
		if err := runIn(dir, emitArgs, &emitBuf); err != nil {
			t.Fatalf("%+v: cross-emit: %v", coord, err)
		}
		if emitBuf.Len() != 0 {
			t.Fatalf("%+v: cross-emit не должен ничего печатать, получили: %s", coord, emitBuf.String())
		}

		acceptArgs := append(cell(coord.format, coord.change, coord.direction), "--op=cross-accept", "--writer-lang=go")
		crossRows := linesIn(t, dir, acceptArgs...)

		compatRows := probeLines(t, cell(coord.format, coord.change, coord.direction)...)

		if len(crossRows) != len(compatRows) {
			t.Fatalf("%+v: разное число строк: cross=%d compat=%d", coord, len(crossRows), len(compatRows))
		}
		for i := range crossRows {
			cr, pr := crossRows[i], compatRows[i]
			if cr.Kind != "cross" {
				t.Fatalf("%+v запись %d: kind=%q, ожидали \"cross\"", coord, i, cr.Kind)
			}
			if cr.Writer != "go" || cr.Reader != "go" {
				t.Fatalf("%+v запись %d: writer/reader = %q/%q, ожидали go/go", coord, i, cr.Writer, cr.Reader)
			}
			if cr.Outcome != pr.Outcome {
				t.Fatalf("%+v запись %d: cross outcome=%q, compat outcome=%q — контрольная клетка разошлась с осью эволюции",
					coord, i, cr.Outcome, pr.Outcome)
			}
			if cr.Bytes != pr.Bytes {
				t.Fatalf("%+v запись %d: cross bytes=%d, compat bytes=%d", coord, i, cr.Bytes, pr.Bytes)
			}
		}
	}
}

// TestRunCrossAcceptFailsWithoutPriorEmit — приём без файла обмена —
// сбой пробы (файла обмена попросту нет), а не находка о формате.
func TestRunCrossAcceptFailsWithoutPriorEmit(t *testing.T) {
	crossExchangeDir(t)
	var buf bytes.Buffer
	args := append(cell("avro", "rename", "newer_reader"), "--op=cross-accept", "--writer-lang=java")
	if err := runIn(schemasDir(t), args, &buf); err == nil {
		t.Fatal("ожидали отказ: файла обмена от java на этих координатах никто не писал")
	} else if buf.Len() != 0 {
		t.Fatalf("вывода быть не должно при отказе пробы: %s", buf.String())
	}
}

// TestRunCrossAcceptDetectsWrongWriterLang — если байты писал "go", а
// принять просят "как будто от java", файла с таким именем не найдётся:
// имя файла обмена детерминированно зависит от языка-писателя.
func TestRunCrossAcceptDetectsWrongWriterLang(t *testing.T) {
	crossExchangeDir(t)
	dir := schemasDir(t)
	emitArgs := append(cell("protobuf", "unknown_field", "newer_writer"), "--op=cross-emit")
	var emitBuf bytes.Buffer
	if err := runIn(dir, emitArgs, &emitBuf); err != nil {
		t.Fatalf("cross-emit: %v", err)
	}
	acceptArgs := append(cell("protobuf", "unknown_field", "newer_writer"), "--op=cross-accept", "--writer-lang=java")
	var buf bytes.Buffer
	if err := runIn(dir, acceptArgs, &buf); err == nil {
		t.Fatal("ожидали отказ: писал go, а принять просили как будто от java")
	}
}

func TestRunIdentityRequiresSameDirection(t *testing.T) {
	crossExchangeDir(t)
	for _, direction := range []string{"newer_reader", "newer_writer"} {
		var buf bytes.Buffer
		args := append(cell("avro", "add_default", direction), "--op=identity")
		if err := runIn(schemasDir(t), args, &buf); err == nil {
			t.Fatalf("направление %s: ожидали отказ — идентичность меряется на одной схеме", direction)
		}
	}
}

// TestRunIdentityControlEqualForAllFormats — контроль обязан быть
// зелёным для всех четырёх плеч НА ЭТОЙ реализации: без этого сравнение
// байтов между реализациями (задача разбора scripts/analyze-cross.py)
// вообще не имеет смысла запускать (spec.md §17.6, «проба идентичности
// недействительна без контроля»).
func TestRunIdentityControlEqualForAllFormats(t *testing.T) {
	crossExchangeDir(t)
	for _, format := range []string{"json", "json-schema", "avro", "protobuf"} {
		var buf bytes.Buffer
		args := append(cell(format, "base", "same"), "--op=identity")
		if err := runIn(schemasDir(t), args, &buf); err != nil {
			t.Fatalf("%s: identity: %v", format, err)
		}
		var res identityResult
		if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &res); err != nil {
			t.Fatalf("%s: строка identity — не валидный JSON: %v (%s)", format, err, buf.String())
		}
		if res.Kind != "identity-probe" {
			t.Fatalf("%s: kind=%q, ожидали identity-probe", format, res.Kind)
		}
		if !res.ControlEqual {
			t.Fatalf("%s: контроль не зелёный — кодек Go недетерминирован на собственных повторных вызовах", format)
		}
		if res.SHA256 == "" || res.Bytes == 0 {
			t.Fatalf("%s: пустой дайджест или нулевой размер: %+v", format, res)
		}
	}
}
