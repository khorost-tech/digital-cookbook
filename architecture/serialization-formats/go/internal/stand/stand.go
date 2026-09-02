// Package stand — данные самого стенда и корень доверия к ним.
//
// Всё, что здесь описано, — решения СТЕНДА, а не форматов. Вызывающая
// сторона их не выбирает, а узнаёт: какие файлы стенду принадлежат,
// какую клетку описывает каждая схема, какие записи канонические.
//
// Круг правок 5 добавил к этому главное — манифест. До него имя клетки
// выводилось из ИМЕНИ ФАЙЛА, и копия штатной схемы под чужим именем
// переименовывала клетку: каталог канонический, записи нетронуты,
// привычных признаков подделки нет. Пять кругов подряд закрывались
// проявления одной находки; манифест закрывает её корень — имя связано с
// содержимым, и подмена превращается в правку манифеста, то есть в
// обычный диф, который видно в ревью.
//
// Вторая польза манифеста может оказаться важнее первой: он ловит
// неумышленное — испорченную копию, недописанный файл, перепутанную
// версию. Такие случаи дают правдоподобную таблицу, целиком неверную.
//
// Языконезависимое описание этих же правил — schemas/spec.md.
package stand

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ManifestFileName — корень доверия стенда. Сам манифест дайджестом не
// защищён и защищён быть не может: его достоверность обеспечивается тем,
// что он лежит в репозитории и всякая его правка видна в ревью как
// обычный диф.
const ManifestFileName = "manifest.json"

// RecordsFileName — имя файла с каноническими записями. Ищется рядом со
// схемой писателя: схемы и записи — части одного набора данных и всегда
// лежат вместе.
const RecordsFileName = "records.json"

// Роли файлов стенда.
const (
	RoleSchema  = "schema"  // файл схемы, по которому кодируют и читают
	RoleRecords = "records" // канонические записи
	RoleSource  = "source"  // исходник, из которого собран файл схемы
)

// Entry — запись манифеста об одном файле.
//
// Version и Change заполнены только у файлов схем, и именно они, а не
// строка пути, задают имя клетки.
type Entry struct {
	Name     string `json:"-"`
	Digest   string `json:"digest"`
	Role     string `json:"role"`
	Content  string `json:"content"` // "text" или "binary" — см. FileDigest
	Notation string `json:"notation,omitempty"`
	Version  int    `json:"version,omitempty"`
	Change   string `json:"change,omitempty"`
}

// Manifest — перечень всех файлов стенда с дайджестами.
type Manifest struct {
	Algorithm string           `json:"algorithm"`
	Files     map[string]Entry `json:"files"`

	dir string
}

// LoadManifest читает манифест каталога стенда.
func LoadManifest(dir string) (*Manifest, error) {
	path := filepath.Join(dir, ManifestFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("манифест стенда %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("манифест стенда %s: %w", path, err)
	}
	if m.Algorithm != "sha256" {
		return nil, fmt.Errorf("манифест стенда %s: неизвестный алгоритм дайджеста %q", path, m.Algorithm)
	}
	if len(m.Files) == 0 {
		return nil, fmt.Errorf("манифест стенда %s: пустой перечень файлов", path)
	}
	m.dir = dir
	for name, e := range m.Files {
		e.Name = name
		m.Files[name] = e
	}
	return &m, nil
}

// Dir возвращает каталог, которому принадлежит манифест.
func (m *Manifest) Dir() string { return m.dir }

// ReadFile читает файл стенда РОВНО ОДИН РАЗ и сверяет дайджест по тем
// самым байтам, которые возвращает.
//
// Круг правок 6. До него сверка и использование были разными чтениями:
// манифест открывал файл, чтобы посчитать дайджест, потом его открывал
// разбор схемы, потом кодек — три-пять проходов на пробу. Между первым
// и последним оставалась щель, и щель не теоретическая: восемь прогонов
// из трёхсот успели напечатать чистые «ok», пройдя сверку и прочитав
// затем другое содержимое. Теперь сверенные байты и есть те, с которыми
// работают дальше; взять содержимое где-то ещё неоткуда.
//
// Расхождение — не предупреждение, а отказ работать: проба, которая
// всё-таки посчитала бы клетку на подменённых данных, напечатала бы
// правдоподобную строку, а правдоподобная неверная строка хуже
// отсутствующей.
func (m *Manifest) ReadFile(name string) ([]byte, error) {
	e, ok := m.Files[name]
	if !ok {
		return nil, fmt.Errorf("файла %q нет в манифесте стенда — стенду принадлежит только то, что в манифесте перечислено", name)
	}
	raw, err := os.ReadFile(filepath.Join(m.dir, name))
	if err != nil {
		return nil, fmt.Errorf("файл стенда %q: %w", name, err)
	}
	got := digestOf(raw, e.Content == "binary")
	if got != e.Digest {
		return nil, fmt.Errorf(
			"содержимое файла %q не совпадает с манифестом стенда (записан %s, посчитан %s) — файл подменён, испорчен или недописан",
			name, short(e.Digest), short(got))
	}
	return raw, nil
}

// VerifyFile — та же сверка, когда содержимое не нужно.
func (m *Manifest) VerifyFile(name string) error {
	_, err := m.ReadFile(name)
	return err
}

// Changes — девять изменений схемы плюс base: базовая версия, которая
// ничего не меняет и участвует только в управляющем случае «схема
// писателя и читателя — одна и та же».
//
// alias_conflict и retype_message добавлены кругом правок (задача
// 6bis) — оба опровергают "слишком красивые нули" исходного набора
// семи изменений: alias_conflict показывает, что модель ожидания
// (правила разрешения схем Avro) не тождественна поведению настоящей
// библиотеки Avro; retype_message показывает случай Protobuf, где
// смена типа поля НЕ меняет тип провода (string и embedded message —
// оба LEN), и объяснение "несовпадение типа провода не даёт ошибки"
// не работает — см. schemas/spec.md, раздел про ограничение метода.
var Changes = []string{"base", "add_default", "add_nodefault", "remove",
	"rename", "retype", "reuse_tag", "unknown_field",
	"alias_conflict", "retype_message"}

// Directions — направления пробы.
var Directions = []string{"same", "newer_reader", "newer_writer"}

// NotationFor возвращает нотацию, схемами которой гоняется плечо.
//
// Контрольное плечо схему не читает вовсе, но клетку всё равно надо
// чем-то назвать, а вырожденность — чем-то определить; поэтому оно
// ходит схемами JSON Schema. Это же плечо, с которым его сравнивают по
// размеру, так что выбор не произволен.
func NotationFor(format string) (string, error) {
	switch format {
	case "avro", "protobuf", "json-schema":
		return format, nil
	case "json":
		return "json-schema", nil
	default:
		return "", fmt.Errorf("неизвестное плечо %q", format)
	}
}

// Resolve находит пару схем по КООРДИНАТАМ КЛЕТКИ.
//
// Круг правок 6, главная правка. Раньше проба принимала пути к файлам,
// и вызывающая сторона задавала и каталог, и — независимо от него —
// плечо. Отсюда сразу два рычага: схемы одной нотации можно было
// подсунуть кодеку другой, а каталог подменить целиком, не оставив
// следа в репозитории. Теперь координаты клетки — единственный вход, а
// схемы стенд находит сам: нотация выводится из плеча, версии и
// изменение — из направления и изменения. Перекрёстное плечо стало
// невозможным по построению, а подставной каталог передавать некуда.
func (m *Manifest) Resolve(format, change, direction string) (writer, reader Entry, err error) {
	notation, err := NotationFor(format)
	if err != nil {
		return Entry{}, Entry{}, err
	}
	if !contains(Changes, change) {
		return Entry{}, Entry{}, fmt.Errorf("неизвестное изменение %q, стенд знает только %s", change, strings.Join(Changes, ", "))
	}
	if !contains(Directions, direction) {
		return Entry{}, Entry{}, fmt.Errorf("неизвестное направление %q, стенд знает только %s", direction, strings.Join(Directions, ", "))
	}

	base := func() (Entry, error) { return m.findSchema(notation, 1, "") }
	changed := func() (Entry, error) { return m.findSchema(notation, 2, change) }

	switch direction {
	case "newer_reader", "newer_writer":
		if change == "base" {
			return Entry{}, Entry{}, fmt.Errorf(
				"изменение base не имеет второй версии — оно осмысленно только с направлением same")
		}
		w, err := base()
		if err != nil {
			return Entry{}, Entry{}, err
		}
		r, err := changed()
		if err != nil {
			return Entry{}, Entry{}, err
		}
		if direction == "newer_reader" {
			return w, r, nil
		}
		return r, w, nil
	default: // same: обе схемы — одна и та же запись манифеста
		var e Entry
		if change == "base" {
			e, err = base()
		} else {
			e, err = changed()
		}
		if err != nil {
			return Entry{}, Entry{}, err
		}
		return e, e, nil
	}
}

// findSchema ищет ровно одну запись манифеста с такими свойствами.
// Ни нуля, ни двух быть не должно: и то, и другое означает, что стенд
// описан неверно, а не что клетку нельзя посчитать.
func (m *Manifest) findSchema(notation string, version int, change string) (Entry, error) {
	var found []Entry
	for _, e := range m.Files {
		if e.Role == RoleSchema && e.Notation == notation && e.Version == version && e.Change == change {
			found = append(found, e)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return Entry{}, fmt.Errorf("в манифесте стенда нет схемы: нотация %s, версия %d, изменение %q",
			notation, version, change)
	default:
		names := make([]string, 0, len(found))
		for _, e := range found {
			names = append(names, e.Name)
		}
		sort.Strings(names)
		return Entry{}, fmt.Errorf("в манифесте стенда несколько схем для нотации %s, версии %d, изменения %q: %s",
			notation, version, change, strings.Join(names, ", "))
	}
}

// recordsDoc — форма records.json. Обе версии описаны одинаково, чтобы у
// доступа к ним не было двух разных форм.
type recordsDoc struct {
	V1 recordSet            `json:"v1"`
	V2 map[string]recordSet `json:"v2"`
}

type recordSet struct {
	Records []map[string]any `json:"records"`
}

// Records возвращает канонические записи для схемы писателя — все, в
// объявленном файлом порядке. Порядок значим: номер записи входит в имя
// строки результата.
//
// Набор выбирается версией и изменением схемы писателя, а не
// направлением: запись всегда имеет форму того, кто её пишет.
func (m *Manifest) Records(writer Entry) ([]map[string]any, error) {
	raw, err := m.ReadFile(RecordsFileName)
	if err != nil {
		return nil, err
	}
	path := RecordsFileName
	var doc recordsDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("канонические записи %s: %w", path, err)
	}

	var set recordSet
	if writer.Version == 1 {
		set = doc.V1
	} else {
		s, ok := doc.V2[writer.Change]
		if !ok {
			return nil, fmt.Errorf("канонические записи %s: нет набора для изменения %q", path, writer.Change)
		}
		set = s
	}
	if len(set.Records) == 0 {
		return nil, fmt.Errorf("канонические записи %s: пустой набор для схемы %s — проверять нечего",
			path, writer.Name)
	}
	return set.Records, nil
}

// FileDigest считает дайджест содержимого файла.
//
// Текстовые файлы хешируются с приведёнными концами строк: стенд живёт в
// git, а git на разных платформах отдаёт один и тот же файл то с CRLF,
// то с LF. Без приведения манифест был бы верен ровно на одной
// операционной системе, а на другой стенд отказывался бы работать на
// собственных, ничем не тронутых данных. Двоичные файлы (собранные
// дескрипторы) хешируются как есть — их git не преобразует, а приведение
// испортило бы содержимое.
func FileDigest(path string, binary bool) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return digestOf(raw, binary), nil
}

// digestOf — то же самое по уже прочитанным байтам. Именно этот вариант
// работает в пробе: сверяется ровно то содержимое, которое пойдёт
// дальше (круг правок 6).
func digestOf(raw []byte, binary bool) string {
	if !binary {
		raw = []byte(strings.ReplaceAll(string(raw), "\r\n", "\n"))
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// CellKey — самодостаточное имя ОДНОЙ строки результата.
//
// Сборщик, склеивающий строки по тройке «плечо, изменение,
// направление», иначе получает на одной тройке две строки с разными
// исходами — обычную и круговую — и не может их развести. Отдельного
// поля с видом пробы для этого мало: склейка идёт по имени, значит вид
// пробы обязан быть В имени. Заодно в нём язык (две реализации пишут в
// один поток результатов) и номер записи.
func CellKey(lang, op, format, change, direction string, recordIndex int) string {
	if change == "" {
		change = "-"
	}
	return fmt.Sprintf("%s/%s/%s/%s/%s/%d", lang, op, format, change, direction, recordIndex)
}

func short(digest string) string {
	if len(digest) <= 12 {
		return digest
	}
	return digest[:12]
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
