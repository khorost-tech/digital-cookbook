// Команда needprobe — проба оси 3 (Задача 7): что нужно ИМЕТЬ, чтобы
// прочитать запись, и первая внешняя зависимость стенда — реестр схем
// Apicurio.
//
// В отличие от probe (compat/roundtrip/size), эта проба не самодостаточна:
// часть её шагов требует живого реестра по сети, а часть — намеренно
// требует его ОТСУТСТВИЯ (это и есть доказательство недоступностью,
// см. task-7-brief.md). Поэтому вместо одной клетки на вызов здесь
// несколько НЕЗАВИСИМЫХ шагов (--step), которые оркестрирующий сценарий
// (bench/run-need-schema.sh) вызывает в нужном порядке, поднимая и гася
// реестр между ними:
//
//   - registry-matrix — требование 1: точка отказа реестра ДО записи.
//     Реестр поднят. Гоняет девять изменений схемы через
//     COMPATIBILITY=BACKWARD, свежий артефакт на каждое изменение (см.
//     doc.go registry — состояние между изменениями НЕ переносится).
//   - produce — готовит "провод" для остальных шагов: регистрирует
//     базовую схему, кодирует каноническую запись №0 и заворачивает её в
//     конверт реестра (envelope.go). Реестр поднят.
//   - need --leg=registry_up   — реестр поднят, читаем через него.
//   - need --leg=registry_down — реестр ПОГАШЕН, та же попытка чтения
//     обязана провалиться без запасного пути (требование 2).
//   - need --leg=schema_local  — реестр не участвует вовсе: схема берётся
//     из локального файла стенда.
//   - envelope   — требование 3: наивный декодер, не знающий о конверте.
//   - need-other — форматы без реестра (protobuf нужен дескриптор без
//     реестра; json/json-schema не нуждаются в схеме для ЧТЕНИЯ вовсе).
//
// Языконезависимое описание решений — schemas/spec.md, раздел «Ось 3».
package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"tech.khorost/serialization-formats/internal/codec"
	"tech.khorost/serialization-formats/internal/probe"
	"tech.khorost/serialization-formats/internal/registry"
	"tech.khorost/serialization-formats/internal/stand"
)

const lang = "go"

// artifactGroup — единственная группа реестра, которой пользуется
// стенд. Apicurio создаёt её неявно при первом обращении.
const artifactGroup = "default"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	dir, err := resolveStandRoot()
	if err != nil {
		return err
	}
	return runIn(dir, args, out)
}

// runIn — то же самое для заранее известного каталога стенда (тесты).
func runIn(standDir string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("needprobe", flag.ContinueOnError)
	step := fs.String("step", "", "шаг: registry-matrix | produce | need | envelope | need-other")
	registryURL := fs.String("registry", "", "базовый URL реестра Apicurio, напр. http://localhost:18091")
	envelopeFile := fs.String("envelope-file", "", "файл с конвертом, подготовленным шагом produce")
	outFile := fs.String("out", "", "куда записать результат шага produce")
	leg := fs.String("leg", "", "плечо шага need: registry_up | registry_down | schema_local")
	if err := fs.Parse(args); err != nil {
		return err
	}

	manifest, err := stand.LoadManifest(standDir)
	if err != nil {
		return err
	}

	switch *step {
	case "registry-matrix":
		return stepRegistryMatrix(manifest, *registryURL, out)
	case "produce":
		return stepProduce(manifest, *registryURL, *outFile)
	case "need":
		return stepNeed(manifest, *leg, *registryURL, *envelopeFile, out)
	case "envelope":
		return stepEnvelope(manifest, *envelopeFile, out)
	case "need-other":
		return stepNeedOther(manifest, out)
	default:
		return fmt.Errorf("--step: неизвестное значение %q", *step)
	}
}

// resolveStandRoot — тот же принцип, что у cmd/probe: каталог стенда
// узнаётся, а не выбирается (schemas/spec.md §4.2).
func resolveStandRoot() (string, error) {
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
			return "", fmt.Errorf("каталог стенда не найден рядом с %s и выше", exe)
		}
		dir = parent
	}
}

// ---- registry-matrix (требование 1) ----------------------------------

// registryMatrixRow — точка отказа реестра, снятая ДО единого байта
// данных: для каждого изменения — свежий артефакт (getBase → правило
// BACKWARD → попытка версии 2), состояние между изменениями НЕ
// переносится (см. doc.go registry). Сравнение с колонкой той же клетки
// матрицы эволюции (schemas/expected.json) выполняет
// scripts/analyze-need.py — сама проба ничего не знает про матрицу,
// иначе сравнение подстроилось бы под то, что уже "должно быть".
type registryMatrixRow struct {
	Kind       string `json:"kind"`
	Change     string `json:"change"`
	Format     string `json:"format"`
	HTTPStatus int    `json:"http_status"`
	// Verdict: "accepted" (2xx — реестр принял версию как совместимую),
	// "rejected" (реестр разобрал схему и нашёл несовместимость,
	// см. RuleViolationException) или "schema_error" (реестр не смог
	// ДАЖЕ оценить совместимость — напр. Apicurio 422
	// UnprocessableSchemaException на alias_conflict, см. spec.md).
	Verdict string `json:"registry_verdict"`
	Detail  string `json:"detail,omitempty"`
}

func stepRegistryMatrix(manifest *stand.Manifest, registryURL string, out io.Writer) error {
	if strings.TrimSpace(registryURL) == "" {
		return fmt.Errorf("--registry обязателен для --step=registry-matrix")
	}
	baseEntry, _, err := manifest.Resolve("avro", "base", "same")
	if err != nil {
		return err
	}
	baseSchema, err := readSchema(manifest, baseEntry)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(out)
	for _, change := range avroChanges() {
		row, err := registryMatrixOneChange(manifest, registryURL, baseSchema, change)
		if err != nil {
			return err
		}
		if err := enc.Encode(row); err != nil {
			return err
		}
	}
	return nil
}

// avroChanges — девять изменений схемы, без base (у базовой версии нет
// второй половины пары, ровно как в матрице эволюции, spec.md §4.3).
func avroChanges() []string {
	all := stand.Changes
	out := make([]string, 0, len(all)-1)
	for _, c := range all {
		if c != "base" {
			out = append(out, c)
		}
	}
	return out
}

func registryMatrixOneChange(manifest *stand.Manifest, registryURL string, baseSchema codec.Schema, change string) (registryMatrixRow, error) {
	changedEntry, _, err := manifest.Resolve("avro", change, "same")
	// same+change=v2: обе схемы — версия 2 этого изменения (см.
	// Manifest.Resolve). Нам нужен файл ИМЕННО версии 2 — берём его.
	if err != nil {
		return registryMatrixRow{}, err
	}
	changedSchema, err := readSchema(manifest, changedEntry)
	if err != nil {
		return registryMatrixRow{}, err
	}

	client := registry.New(registryURL)
	// Артефакт свежий на КАЖДОЕ изменение: одно и то же имя артефакта,
	// использованное дважды, дало бы реестру другую историю версий и
	// другой ответ — состояние между "прогонами" одного артефакта не
	// переносится (task-7-brief.md, требование к сценарию).
	artifactID := "user-need-matrix-" + change
	_, createStatus, err := client.CreateArtifact(artifactGroup, artifactID, "AVRO", string(baseSchema.Bytes), "application/json")
	if err != nil {
		return registryMatrixRow{}, fmt.Errorf("registry-matrix: %s: создание артефакта: %w", change, err)
	}
	if createStatus != 200 {
		return registryMatrixRow{}, fmt.Errorf("registry-matrix: %s: создание артефакта вернуло %d", change, createStatus)
	}
	if _, err := client.SetCompatibilityRule(artifactGroup, artifactID, "BACKWARD"); err != nil {
		return registryMatrixRow{}, fmt.Errorf("registry-matrix: %s: правило совместимости: %w", change, err)
	}

	_, status, body, err := client.AddVersion(artifactGroup, artifactID, string(changedSchema.Bytes), "application/json")
	if err != nil {
		return registryMatrixRow{}, fmt.Errorf("registry-matrix: %s: попытка версии 2: %w", change, err)
	}

	verdict := "rejected"
	switch {
	case status >= 200 && status < 300:
		verdict = "accepted"
	case status == 422:
		verdict = "schema_error"
	}
	return registryMatrixRow{
		Kind: "registry_matrix", Change: change, Format: "avro",
		HTTPStatus: status, Verdict: verdict, Detail: body,
	}, nil
}

// ---- produce -----------------------------------------------------------

// envelopeArtifact — то, что шаг produce оставляет для последующих
// шагов need/envelope. Это НЕ строка фикстуры (не несёт kind=need и не
// пишется в fixtures/need.txt) — промежуточный артефакт оркестрации,
// как собранный бинарник или .desc-файл.
type envelopeArtifact struct {
	RegistryURL      string         `json:"registry_url"`
	GlobalID         int64          `json:"global_id"`
	RawAvroLen       int            `json:"raw_avro_len"`
	EnvelopeLen      int            `json:"envelope_len"`
	MeasuredPrefix   int            `json:"measured_prefix_len"`
	EnvelopeB64      string         `json:"envelope_b64"`
	RawAvroB64       string         `json:"raw_avro_b64"`
	Record           map[string]any `json:"record"`
	LocalSchemaEntry string         `json:"local_schema_entry"`
}

func stepProduce(manifest *stand.Manifest, registryURL, outFile string) error {
	if strings.TrimSpace(registryURL) == "" {
		return fmt.Errorf("--registry обязателен для --step=produce")
	}
	if strings.TrimSpace(outFile) == "" {
		return fmt.Errorf("--out обязателен для --step=produce")
	}
	baseEntry, _, err := manifest.Resolve("avro", "base", "same")
	if err != nil {
		return err
	}
	baseSchema, err := readSchema(manifest, baseEntry)
	if err != nil {
		return err
	}
	records, err := manifest.Records(baseEntry)
	if err != nil {
		return err
	}
	rec, _ := codec.Normalize(records[0]).(map[string]any)

	c, err := codec.New("avro")
	if err != nil {
		return err
	}
	if err := codec.PrepareSchema(c, baseSchema); err != nil {
		return err
	}
	rawAvro, err := c.Encode(rec, baseSchema)
	if err != nil {
		return fmt.Errorf("produce: кодирование канонической записи 0: %w", err)
	}

	client := registry.New(registryURL)
	// Свежий артефакт: produce вызывается ровно один раз за прогон
	// bench-сценария, но имя всё равно уникально своей ролью, а не
	// изменением схемы, — так шаг можно перевызвать при отладке, не
	// зацепив артефакты registry-matrix.
	artifactID := "user-need-produce"
	globalID, status, err := client.CreateArtifact(artifactGroup, artifactID, "AVRO", string(baseSchema.Bytes), "application/json")
	if err != nil {
		return fmt.Errorf("produce: регистрация базовой схемы: %w", err)
	}
	if status != 200 {
		return fmt.Errorf("produce: регистрация базовой схемы вернула %d", status)
	}

	envelope := wrapEnvelope(globalID, rawAvro)

	art := envelopeArtifact{
		RegistryURL:      registryURL,
		GlobalID:         globalID,
		RawAvroLen:       len(rawAvro),
		EnvelopeLen:      len(envelope),
		MeasuredPrefix:   len(envelope) - len(rawAvro),
		EnvelopeB64:      base64.StdEncoding.EncodeToString(envelope),
		RawAvroB64:       base64.StdEncoding.EncodeToString(rawAvro),
		Record:           rec,
		LocalSchemaEntry: baseEntry.Name,
	}
	raw, err := json.MarshalIndent(art, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(outFile, raw, 0o644)
}

func loadEnvelopeArtifact(path string) (envelopeArtifact, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return envelopeArtifact{}, fmt.Errorf("файл конверта %s: %w", path, err)
	}
	var art envelopeArtifact
	if err := json.Unmarshal(raw, &art); err != nil {
		return envelopeArtifact{}, fmt.Errorf("файл конверта %s: %w", path, err)
	}
	// encoding/json отдаёт числа float64 независимо от того, что записал
	// шаг produce (там это был int64 после codec.Normalize). Без
	// повторной нормализации "id":1 в артефакте и "id":1 из декодера
	// оказались бы РАЗНЫХ категорий (дробное против целого, spec.md §5.1)
	// и сравнение молча решило бы "wrong" там, где на самом деле "ok" —
	// это была бы НАША порча, предъявленная как находка про Avro.
	if norm, ok := codec.Normalize(art.Record).(map[string]any); ok {
		art.Record = norm
	}
	return art, nil
}

// ---- need (требования 2 и 4) -------------------------------------------

type needRow struct {
	Kind            string         `json:"kind"`
	Format          string         `json:"format"`
	Leg             string         `json:"leg"`
	SchemaAvailable bool           `json:"schema_available"`
	Outcome         string         `json:"outcome"`
	RegistryCalls   int            `json:"registry_calls"`
	Got             map[string]any `json:"got,omitempty"`
	Want            map[string]any `json:"want,omitempty"`
	Error           string         `json:"error,omitempty"`
}

func stepNeed(manifest *stand.Manifest, leg, registryURL, envelopeFile string, out io.Writer) error {
	if strings.TrimSpace(envelopeFile) == "" {
		return fmt.Errorf("--envelope-file обязателен для --step=need")
	}
	art, err := loadEnvelopeArtifact(envelopeFile)
	if err != nil {
		return err
	}
	globalID, avroBytes, err := unwrapEnvelope(mustB64(art.EnvelopeB64))
	if err != nil {
		return fmt.Errorf("need: конверт %s повреждён: %w", envelopeFile, err)
	}
	if globalID != art.GlobalID {
		return fmt.Errorf("need: globalID в конверте (%d) не совпадает с записанным в артефакте (%d)", globalID, art.GlobalID)
	}

	row := needRow{Kind: "need", Format: "avro", Leg: leg, Want: art.Record}

	switch leg {
	case "registry_up":
		if strings.TrimSpace(registryURL) == "" {
			return fmt.Errorf("--registry обязателен для --leg=registry_up")
		}
		client := registry.New(registryURL)
		schemaText, status, err := client.FetchByGlobalID(art.GlobalID)
		row.RegistryCalls = client.Calls()
		if err != nil {
			// Реестр поднят, но обращение всё равно не удалось — это
			// СБОЙ шага оркестрации (registry_up подразумевает живой
			// реестр), а не находка требования 2. Печатать нечего.
			return fmt.Errorf("need(registry_up): реестр объявлен поднятым, но обращение не удалось: %w", err)
		}
		if status != 200 {
			return fmt.Errorf("need(registry_up): неожиданный статус %d при чтении схемы", status)
		}
		row.SchemaAvailable = true
		decodeAvroInto(&row, schemaText, avroBytes, art.Record)

	case "registry_down":
		// ГЛАВНАЯ строка требования 2. Реестр в этот момент должен быть
		// физически погашен сценарием (bench/run-need-schema.sh) — проба
		// сама его не гасит и не поднимает, только честно отражает, что
		// произошло при попытке. Никакого запасного пути к схеме здесь
		// нет: если бы декодирование всё-таки состоялось, это означало
		// бы, что схема пришла ОТКУДА-ТО ЕЩЁ (см. ниже про
		// "недействительную пробу").
		if strings.TrimSpace(registryURL) == "" {
			return fmt.Errorf("--registry обязателен для --leg=registry_down (тот же адрес, что и для registry_up — гасится контейнер, а не адрес)")
		}
		client := registry.New(registryURL)
		_, _, fetchErr := client.FetchByGlobalID(art.GlobalID)
		row.RegistryCalls = client.Calls()
		if fetchErr == nil {
			// Проба НЕДЕЙСТВИТЕЛЬНА: реестр должен был быть недоступен.
			// Не выводим "схема не нужна" — объявляем находку негодной
			// (task-7-brief.md: "если при недоступной схеме прочиталось
			// всё... проба недействительна").
			row.SchemaAvailable = true
			row.Outcome = "invalid_probe"
			row.Error = "реестр ответил, хотя ожидался погашенным — эта строка ничего не доказывает про доступность схемы"
			break
		}
		row.SchemaAvailable = false
		row.Outcome = "unavailable"
		row.Error = fetchErr.Error()

	case "schema_local":
		// Реестр вообще не участвует: схема берётся из локального файла
		// стенда (тот, что уже есть в schemas/manifest.json) — ноль
		// обращений к реестру.
		entry, _, err := manifest.Resolve("avro", "base", "same")
		if err != nil {
			return err
		}
		schema, err := readSchema(manifest, entry)
		if err != nil {
			return err
		}
		row.SchemaAvailable = true
		decodeAvroInto(&row, string(schema.Bytes), avroBytes, art.Record)

	default:
		return fmt.Errorf("--leg: неизвестное значение %q (registry_up | registry_down | schema_local)", leg)
	}

	enc := json.NewEncoder(out)
	return enc.Encode(row)
}

// decodeAvroInto декодирует avro-байты (БЕЗ префикса конверта) уже
// известной схемой писателя=читателя (direction=same — реестр отдаёт
// схему, которой запись и была закодирована, других версий тут нет) и
// сравнивает результат с ожиданием.
func decodeAvroInto(row *needRow, schemaText string, avroBytes []byte, want map[string]any) {
	schema := codec.Schema{Name: "fetched", Notation: codec.NotationAvro, Bytes: []byte(schemaText)}
	c, err := codec.New("avro")
	if err != nil {
		row.Outcome = "error"
		row.Error = err.Error()
		return
	}
	if err := codec.PrepareSchema(c, schema); err != nil {
		row.Outcome = "error"
		row.Error = err.Error()
		return
	}
	got, err := c.Decode(avroBytes, schema, schema)
	if err != nil {
		row.Outcome = "refused"
		row.Error = err.Error()
		return
	}
	row.Got = got
	if probe.RecordsEqual(got, want) {
		row.Outcome = "ok"
	} else {
		row.Outcome = "wrong"
	}
}

// ---- envelope (требование 3) --------------------------------------------

type envelopeRow struct {
	Kind      string         `json:"kind"`
	Decoder   string         `json:"decoder"`
	Outcome   string         `json:"outcome"`
	PrefixLen int            `json:"prefix_len"`
	Got       map[string]any `json:"got,omitempty"`
	Want      map[string]any `json:"want,omitempty"`
	Error     string         `json:"error,omitempty"`
}

func stepEnvelope(manifest *stand.Manifest, envelopeFile string, out io.Writer) error {
	if strings.TrimSpace(envelopeFile) == "" {
		return fmt.Errorf("--envelope-file обязателен для --step=envelope")
	}
	art, err := loadEnvelopeArtifact(envelopeFile)
	if err != nil {
		return err
	}
	envelope := mustB64(art.EnvelopeB64)

	entry, _, err := manifest.Resolve("avro", "base", "same")
	if err != nil {
		return err
	}
	schema, err := readSchema(manifest, entry)
	if err != nil {
		return err
	}

	row := envelopeRow{Kind: "envelope", Decoder: "naive", PrefixLen: art.MeasuredPrefix}

	c, err := codec.New("avro")
	if err != nil {
		return err
	}
	if err := codec.PrepareSchema(c, schema); err != nil {
		return err
	}
	// НАИВНЫЙ декодер: конверт целиком (5 байт префикса + avro) подаётся
	// в декодер, как если бы префикса не было вовсе — ровно та ошибка,
	// которую совершает читатель, не знающий про формат провода реестра.
	got, decErr := c.Decode(envelope, schema, schema)
	row.Want = art.Record
	switch {
	case decErr != nil:
		row.Outcome = "error"
		row.Error = decErr.Error()
	default:
		// "wrong" обязано нести наблюдаемое значение (тот же принцип, что
		// и в оси эволюции, scripts/analyze-evolution.py) — иначе
		// утверждение "прочиталось, но не то" ничем не подтверждено.
		row.Got = got
		if probe.RecordsEqual(got, art.Record) {
			row.Outcome = "ok"
		} else {
			row.Outcome = "wrong"
		}
	}

	enc := json.NewEncoder(out)
	return enc.Encode(row)
}

// ---- need-other: protobuf/json/json-schema без реестра ------------------

func stepNeedOther(manifest *stand.Manifest, out io.Writer) error {
	enc := json.NewEncoder(out)

	// protobuf: leg=schema_local — дескриптор есть локально (тот же файл,
	// что использует ось эволюции), декодирование удаётся.
	if err := needOtherProtobufLocal(manifest, enc); err != nil {
		return err
	}
	// protobuf: leg=no_schema — дескриптор НЕДОСТУПЕН (симулируется
	// пустым содержимым: у protobuf в этом стенде схема распространяется
	// не через реестр, а вместе со сборкой, поэтому "недоступность" для
	// него моделируется иначе, чем для avro, но вопрос тот же — можно ли
	// прочитать байты, не зная схемы).
	if err := needOtherProtobufMissing(enc); err != nil {
		return err
	}
	// json: контроль — схема не нужна никогда (spec.md §7.1).
	if err := needOtherJSON(manifest, "json", enc); err != nil {
		return err
	}
	// json-schema: те же самые байты (spec.md §7.2 — "байты те же, что у
	// контроля"), декодируются ПЛОСКИМ json-кодеком, ни разу не тронув
	// файл схемы, — доказательство, что схема JSON Schema нужна только
	// для ВАЛИДАЦИИ, а не для чтения.
	if err := needOtherJSON(manifest, "json-schema", enc); err != nil {
		return err
	}
	return nil
}

func needOtherProtobufLocal(manifest *stand.Manifest, enc *json.Encoder) error {
	entry, _, err := manifest.Resolve("protobuf", "base", "same")
	if err != nil {
		return err
	}
	schema, err := readSchema(manifest, entry)
	if err != nil {
		return err
	}
	records, err := manifest.Records(entry)
	if err != nil {
		return err
	}
	rec, _ := codec.Normalize(records[0]).(map[string]any)

	c, err := codec.New("protobuf")
	if err != nil {
		return err
	}
	row := needRow{Kind: "need", Format: "protobuf", Leg: "schema_local", Want: rec}
	if err := codec.PrepareSchema(c, schema); err != nil {
		row.Outcome = "unavailable"
		row.SchemaAvailable = false
		row.Error = err.Error()
		return enc.Encode(row)
	}
	b, err := c.Encode(rec, schema)
	if err != nil {
		return fmt.Errorf("need-other(protobuf/schema_local): кодирование: %w", err)
	}
	got, err := c.Decode(b, schema, schema)
	row.SchemaAvailable = true
	if err != nil {
		row.Outcome = "refused"
		row.Error = err.Error()
	} else {
		row.Got = got
		if probe.RecordsEqual(got, rec) {
			row.Outcome = "ok"
		} else {
			row.Outcome = "wrong"
		}
	}
	return enc.Encode(row)
}

// needOtherProtobufMissing декодирует ТЕ ЖЕ самые байты (закодированные
// нормальной схемой), но БЕЗ дескриптора: пустая схема отдаётся
// декодеру напрямую, минуя PrepareSchema/кэш, — то есть попытка честная,
// а не подстроенная так, чтобы обязательно провалиться на каком-то
// раннем формальном шаге, который к вопросу "нужен ли дескриптор" не
// относится.
func needOtherProtobufMissing(enc *json.Encoder) error {
	empty := codec.Schema{Name: "no-descriptor", Notation: codec.NotationProtobuf, Bytes: []byte{}}
	c, err := codec.New("protobuf")
	if err != nil {
		return err
	}
	row := needRow{Kind: "need", Format: "protobuf", Leg: "no_schema", SchemaAvailable: false}
	prepErr := codec.PrepareSchema(c, empty)
	if prepErr == nil {
		// Пустой FileDescriptorSet разобрался без ошибки (это законное
		// поведение protobuf-библиотеки — пустой набор дескрипторов не
		// является синтаксической ошибкой), тогда пробуем декодировать
		// и ожидаем провал уже здесь, при поиске сообщения.
		if _, err := c.Decode([]byte{0x08, 0x01}, empty, empty); err == nil {
			row.Outcome = "invalid_probe"
			row.Error = "декодирование без дескриптора неожиданно удалось — проба не доказывает недоступностью то, что должна"
			return enc.Encode(row)
		} else {
			row.Outcome = "unavailable"
			row.Error = err.Error()
			return enc.Encode(row)
		}
	}
	if !errors.Is(prepErr, codec.ErrProbeFailure) {
		return fmt.Errorf("need-other(protobuf/no_schema): неожиданная ошибка: %w", prepErr)
	}
	row.Outcome = "unavailable"
	row.Error = prepErr.Error()
	return enc.Encode(row)
}

func needOtherJSON(manifest *stand.Manifest, format string, enc *json.Encoder) error {
	entry, _, err := manifest.Resolve(format, "base", "same")
	if err != nil {
		return err
	}
	records, err := manifest.Records(entry)
	if err != nil {
		return err
	}
	rec, _ := codec.Normalize(records[0]).(map[string]any)

	// Кодирование — штатным кодеком плеча (у json и json-schema байты
	// совпадают, spec.md §7.2), а вот ЧТЕНИЕ идёт ПЛОСКИМ json-кодеком
	// намеренно (см. doc-комментарий stepNeedOther) — со схемой format
	// эта функция не встречается вовсе, даже для json-schema.
	writerCodec, err := codec.New(format)
	if err != nil {
		return err
	}
	writerSchema := codec.Schema{}
	if format == "json-schema" {
		s, err := readSchema(manifest, entry)
		if err != nil {
			return err
		}
		writerSchema = s
		if err := codec.PrepareSchema(writerCodec, writerSchema); err != nil {
			return err
		}
	}
	b, err := writerCodec.Encode(rec, writerSchema)
	if err != nil {
		return fmt.Errorf("need-other(%s): кодирование: %w", format, err)
	}

	plainJSON, err := codec.New("json")
	if err != nil {
		return err
	}
	got, err := plainJSON.Decode(b, codec.Schema{}, codec.Schema{})

	row := needRow{Kind: "need", Format: format, Leg: "no_schema", SchemaAvailable: false, Want: rec}
	if err != nil {
		row.Outcome = "unavailable"
		row.Error = err.Error()
	} else {
		row.Got = got
		if probe.RecordsEqual(got, rec) {
			row.Outcome = "ok"
		} else {
			row.Outcome = "wrong"
		}
	}
	return enc.Encode(row)
}

// ---- мелкие общие помощники ---------------------------------------------

func readSchema(m *stand.Manifest, e stand.Entry) (codec.Schema, error) {
	raw, err := m.ReadFile(e.Name)
	if err != nil {
		return codec.Schema{}, err
	}
	return codec.Schema{Name: e.Name, Notation: e.Notation, Bytes: raw}, nil
}

func mustB64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(fmt.Sprintf("повреждённый base64 в артефакте конверта: %v", err))
	}
	return b
}

// Равенство записей — то же самое правило, что и у остальных проб
// стенда: internal/probe.RecordsEqual (spec.md §5.3). Отдельной копии
// здесь нет намеренно — две копии одного правила неизбежно разъедутся
// незаметно (см. комментарий в internal/codec/normalize.go).
