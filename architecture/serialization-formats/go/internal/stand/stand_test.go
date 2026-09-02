package stand

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func schemasDir(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "..", "schemas"))
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	return p
}

func load(t *testing.T, dir string) *Manifest {
	t.Helper()
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest(%s): %v", dir, err)
	}
	return m
}

// Штатный стенд обязан сходиться с собственным манифестом целиком.
func TestRealStandMatchesItsManifest(t *testing.T) {
	m := load(t, schemasDir(t))
	if m.Algorithm != "sha256" {
		t.Fatalf("algorithm = %q", m.Algorithm)
	}
	if len(m.Files) < 33 {
		t.Fatalf("в манифесте %d записей — ждали все схемы и записи", len(m.Files))
	}
	for name := range m.Files {
		if err := m.VerifyFile(name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// ГЛАВНАЯ ПРАВКА КРУГА 6. Схемы находятся по КООРДИНАТАМ КЛЕТКИ.
// Вызывающая сторона не называет ни файлов, ни каталога — только плечо,
// изменение и направление.
func TestResolveFindsSchemasByCellCoordinates(t *testing.T) {
	m := load(t, schemasDir(t))
	cases := []struct {
		format, change, direction string
		writer, reader            string
	}{
		{"avro", "remove", "newer_reader", "user_v1.avsc", "user_v2_remove.avsc"},
		{"avro", "remove", "newer_writer", "user_v2_remove.avsc", "user_v1.avsc"},
		{"protobuf", "retype", "newer_reader", "user_v1.desc", "user_v2_retype.desc"},
		{"json-schema", "add_default", "newer_writer", "user_v2_add_default.json", "user_v1.json"},
		{"avro", "base", "same", "user_v1.avsc", "user_v1.avsc"},
		{"protobuf", "reuse_tag", "same", "user_v2_reuse_tag.desc", "user_v2_reuse_tag.desc"},
	}
	for _, c := range cases {
		w, r, err := m.Resolve(c.format, c.change, c.direction)
		if err != nil {
			t.Fatalf("%s/%s/%s: %v", c.format, c.change, c.direction, err)
		}
		if w.Name != c.writer || r.Name != c.reader {
			t.Fatalf("%s/%s/%s: получили (%s, %s), ждали (%s, %s)",
				c.format, c.change, c.direction, w.Name, r.Name, c.writer, c.reader)
		}
	}
}

// Перекрёстное плечо стало невозможным по построению: нотация выводится
// из ТОЙ ЖЕ координаты, что выбирает кодек. Раньше «плечо protobuf со
// схемами Avro» задавалось двумя независимыми аргументами.
func TestResolveTiesNotationToFormat(t *testing.T) {
	m := load(t, schemasDir(t))
	for format, ext := range map[string]string{
		"avro": ".avsc", "protobuf": ".desc", "json-schema": ".json",
		// контрольное плечо схему не читает вовсе и ходит схемами
		// JSON Schema — того самого плеча, с которым его сравнивают
		"json": ".json",
	} {
		w, r, err := m.Resolve(format, "remove", "newer_reader")
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if !strings.HasSuffix(w.Name, ext) || !strings.HasSuffix(r.Name, ext) {
			t.Fatalf("%s: получили (%s, %s), ждали схемы %s", format, w.Name, r.Name, ext)
		}
	}
}

func TestResolveRejectsUnknownCoordinates(t *testing.T) {
	m := load(t, schemasDir(t))
	cases := []struct{ format, change, direction, why string }{
		{"xml", "remove", "newer_reader", "неизвестное плечо"},
		{"avro", "нет-такого", "newer_reader", "неизвестное изменение"},
		{"avro", "remove", "вбок", "неизвестное направление"},
		{"avro", "base", "newer_reader", "у base нет второй версии"},
		{"avro", "base", "newer_writer", "у base нет второй версии"},
	}
	for _, c := range cases {
		if _, _, err := m.Resolve(c.format, c.change, c.direction); err == nil {
			t.Fatalf("%s/%s/%s: ожидали отказ (%s)", c.format, c.change, c.direction, c.why)
		}
	}
}

// Круг правок 5, и он же — вторая линия круга 6: копия штатной схемы
// под чужим именем. Каталог передавать теперь некуда, но подмена файла
// ВНУТРИ стенда всё равно обязана останавливать пробу.
func TestReadFileRefusesContentThatDoesNotMatchManifest(t *testing.T) {
	dir := t.TempDir()
	copyStand(t, dir)
	raw, err := os.ReadFile(filepath.Join(dir, "user_v2_add_default.desc"))
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "user_v2_retype.desc"), raw, 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}

	m := load(t, dir)
	_, err = m.ReadFile("user_v2_retype.desc")
	if err == nil {
		t.Fatal("ожидали отказ: содержимое файла не совпадает с записанным в манифесте")
	}
	if !strings.Contains(err.Error(), "манифест") {
		t.Fatalf("ошибка не называет причину: %v", err)
	}
}

// Файл, которого в манифесте нет вовсе, стенду не принадлежит.
func TestReadFileRefusesFileAbsentFromManifest(t *testing.T) {
	dir := t.TempDir()
	copyStand(t, dir)
	raw, err := os.ReadFile(filepath.Join(dir, "user_v1.avsc"))
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "user_v9_selfmade.avsc"), raw, 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}
	m := load(t, dir)
	if _, err := m.ReadFile("user_v9_selfmade.avsc"); err == nil {
		t.Fatal("ожидали отказ: файла нет в манифесте")
	}
}

// Испорченная копия, недописанный файл, перепутанная версия — вторая
// польза манифеста, и она может оказаться важнее первой: такие случаи
// дают правдоподобную таблицу, целиком неверную.
func TestReadFileRefusesTruncatedFile(t *testing.T) {
	dir := t.TempDir()
	copyStand(t, dir)
	raw, err := os.ReadFile(filepath.Join(dir, "user_v1.desc"))
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "user_v1.desc"), raw[:len(raw)/2], 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}
	m := load(t, dir)
	if _, err := m.ReadFile("user_v1.desc"); err == nil {
		t.Fatal("ожидали отказ: файл недописан")
	}
}

// Круг правок 6: сверенные байты и есть те, что возвращаются. Раньше
// сверка и использование были разными чтениями, и между ними
// оставалась щель.
func TestReadFileReturnsTheVeryBytesItVerified(t *testing.T) {
	dir := t.TempDir()
	copyStand(t, dir)
	m := load(t, dir)
	got, err := m.ReadFile("user_v1.avsc")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, "user_v1.avsc"))
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if string(got) != string(onDisk) {
		t.Fatal("вернулось не то содержимое, которое лежит в файле")
	}
	if digestOf(got, false) != m.Files["user_v1.avsc"].Digest {
		t.Fatal("дайджест возвращённых байтов не совпадает с манифестом")
	}
}

// Записи проверяются тем же дайджестом: подменить их так же нельзя.
func TestRecordsAreVerifiedAgainstManifest(t *testing.T) {
	dir := t.TempDir()
	copyStand(t, dir)
	m := load(t, dir)
	e, _, err := m.Resolve("avro", "base", "same")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := m.Records(e); err != nil {
		t.Fatalf("нетронутые записи отвергнуты: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "records.json"))
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	v1 := doc["v1"].(map[string]any)["records"].([]any)
	v1[0].(map[string]any)["id"] = 0 // подобранное значение — рычаг круга 4
	edited, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("сборка: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "records.json"), edited, 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}
	m2 := load(t, dir)
	if _, err := m2.Records(e); err == nil {
		t.Fatal("ожидали отказ: записи не совпадают с манифестом")
	}
}

// Перенос строк не должен влиять на дайджест текстовых файлов: стенд
// живёт в системе контроля версий, а она на разных платформах отдаёт
// разные концы строк. Двоичные файлы хешируются как есть.
func TestTextDigestIsLineEndingAgnostic(t *testing.T) {
	dir := t.TempDir()
	copyStand(t, dir)
	p := filepath.Join(dir, "user_v1.avsc")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	crlf := strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n", "\r\n")
	if err := os.WriteFile(p, []byte(crlf), 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}
	m := load(t, dir)
	if _, err := m.ReadFile("user_v1.avsc"); err != nil {
		t.Fatalf("файл с другими концами строк отвергнут: %v", err)
	}
}

func TestRecordsComeFromCanonicalFile(t *testing.T) {
	m := load(t, schemasDir(t))
	v1e, _, err := m.Resolve("avro", "base", "same")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	v1, err := m.Records(v1e)
	if err != nil {
		t.Fatalf("Records(v1): %v", err)
	}
	if len(v1) != 5 {
		t.Fatalf("v1: %d записей, ждали 5", len(v1))
	}
	if v1[0]["name"] != "Анна" {
		t.Fatalf("v1[0].name = %#v — порядок записей значим", v1[0]["name"])
	}
	for _, c := range []struct {
		format, change string
		fields         []string
	}{
		{"protobuf", "remove", []string{"id", "name"}},
		{"avro", "rename", []string{"contact", "id", "name"}},
		{"protobuf", "reuse_tag", []string{"id", "login_count", "name"}},
	} {
		w, _, err := m.Resolve(c.format, c.change, "newer_writer")
		if err != nil {
			t.Fatalf("Resolve(%s/%s): %v", c.format, c.change, err)
		}
		recs, err := m.Records(w)
		if err != nil {
			t.Fatalf("Records(%s): %v", w.Name, err)
		}
		if len(recs) != 5 {
			t.Fatalf("%s: %d записей, ждали 5", w.Name, len(recs))
		}
		for i, r := range recs {
			if len(r) != len(c.fields) {
				t.Fatalf("%s[%d]: поля %v, ждали %v", w.Name, i, keysOf(r), c.fields)
			}
			for _, f := range c.fields {
				if _, ok := r[f]; !ok {
					t.Fatalf("%s[%d]: нет поля %q", w.Name, i, f)
				}
			}
		}
	}
}

func TestCellKeyIsSelfSufficient(t *testing.T) {
	compat := CellKey("go", "compat", "protobuf", "retype", "newer_reader", 0)
	round := CellKey("go", "roundtrip", "protobuf", "retype", "newer_reader", 0)
	if compat == round {
		t.Fatalf("ключи обычной и круговой пробы совпали: %q", compat)
	}
	if CellKey("go", "compat", "protobuf", "retype", "newer_reader", 1) == compat {
		t.Fatal("ключи двух разных записей одной клетки совпали")
	}
	if CellKey("java", "compat", "protobuf", "retype", "newer_reader", 0) == compat {
		t.Fatal("ключи двух языков совпали")
	}
	for _, part := range []string{"go", "compat", "protobuf", "retype", "newer_reader"} {
		if !strings.Contains(compat, part) {
			t.Fatalf("ключ %q не содержит %q", compat, part)
		}
	}
}

// copyStand делает рабочую копию стенда во временном каталоге вместе с
// манифестом — портить настоящий каталог ради проверок нельзя.
func copyStand(t *testing.T, dst string) {
	t.Helper()
	src := schemasDir(t)
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatalf("чтение %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), raw, 0o644); err != nil {
			t.Fatalf("запись %s: %v", e.Name(), err)
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
