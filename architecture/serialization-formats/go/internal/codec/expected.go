package codec

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/hamba/avro/v2"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ErrDegenerateSchema — сентинел для вырожденного случая: имя файла
// схемы читателя обещает изменение относительно писателя, но в ЭТОЙ
// нотации набор полей у обеих схем структурно совпадает (пример
// стенда — reuse_tag для Avro и JSON Schema: переиспользование НОМЕРА
// поля существует только в Protobuf, у остальных двух user_v2_reuse_tag
// — побитовая копия user_v1 по полям). Реальной, отличной от writer,
// ожидаемой записи в такой паре взять неоткуда — не "ok" и не "wrong",
// а то, что schemas/expected.json помечает "n/a". cmd/probe ловит этот
// сентинел через errors.Is и печатает outcome "n/a", не пытаясь
// прогнать кодек.
var ErrDegenerateSchema = errors.New("схема читателя структурно не отличается от схемы писателя для этого изменения")

// fieldKind — минимальный, общий для всех трёх нотаций стенда набор
// типов. Список исчерпывающий ровно настолько, насколько это нужно
// семи изменениям schemas/ — расширять по необходимости, а не заранее.
type fieldKind int

const (
	kindUnknown fieldKind = iota
	kindInt
	kindString
	kindBool
)

// matchStrategy — то, ЧЕМ поле писателя опознаётся как "то же самое"
// поле читателя. Это единственное, что в ExpectedRecord знает о
// различии нотаций, и знание это не про Avro/Protobuf/JSON Schema по
// названию, а про то, чем в принципе может быть устроено соответствие
// полей на проводе.
type matchStrategy int

const (
	// matchByName — поля совпадают, если совпадают их имена (JSON
	// Schema: alias-механизма нет вовсе, переименование теряет связь).
	matchByName matchStrategy = iota
	// matchByNameOrAlias — совпадают по имени, а если нет — по
	// aliases, объявленным на поле ЧИТАТЕЛЯ (ровно так, как это делает
	// сам Avro при разрешении схем: alias живёт на новой схеме и
	// указывает старое имя).
	matchByNameOrAlias
	// matchByNumber — совпадают по номеру поля, а не по имени: так
	// физически устроен проводной формат Protobuf. Номер — не то же
	// самое, что тождество поля: два РАЗНЫХ по смыслу поля могут
	// делить один номер (reuse_tag), и это надо отличать от настоящего
	// retype (см. ExpectedRecord, "sameIdentity" ниже).
	matchByNumber
)

// schemaField — описание одного поля схемы в терминах, общих для всех
// нотаций стенда.
type schemaField struct {
	Name    string
	Number  int32 // используется только при matchByNumber
	Aliases []string
	Kind    fieldKind
	// Bits — разрядность, объявленная схемой для целого поля (32 или
	// 64). Круг правок 5: без неё «значение соответствует типу поля»
	// проверялось только по категории, и число, не помещающееся в
	// объявленный int32, доходило до кодирования, где молча усекалось.
	// Клетка, в которой схема писателя и читателя ОДНА И ТА ЖЕ,
	// показывала из-за этого «прочиталось, но не то» — наша порча,
	// предъявленная формату. Для нецелых полей значения не имеет.
	Bits       int
	HasDefault bool
	Default    any
	// Required — обязано ли поле физически присутствовать у любого
	// корректного писателя этой схемы. У Avro и Protobuf это всегда
	// true для поля без default (у Avro это буквально так устроено, у
	// Protobuf HasDefault всегда true, и Required там не участвует в
	// выборе ветки). У JSON Schema это НЕ то же самое, что наличие в
	// "properties" — это отдельно объявленный список "required" (круг
	// правок 3: смешение этих двух понятий превратило законный "ok" в
	// "wrong" для полей вроде необязательного "age").
	Required bool
}

// ExpectedRecord строит запись, которую читатель ОБЯЗАН увидеть, если
// формат отработал добросовестно. Считается ТОЛЬКО из записи писателя
// и двух схем — результат декодирования эта функция не видит и видеть
// не должна: иначе ожидание подстроится под наблюдение, и исход wrong
// не сработает никогда (это ровно то, чем оказался нежизнеспособен
// прежний --want в круге правок 1 — см. отчёт).
//
// ВАЖНО (круг правок 3): эта гарантия держится НЕ проверкой сигнатуры
// через reflect (та ловит только буквальное добавление параметра для
// decode-результата, и на практике осталась зелёной при двух живых
// попытках контрабанды) — держат её тесты, прибивающие гвоздями
// конкретные значения для каждого изменения и направления
// (expected_test.go), и сквозные тесты, гоняющие ExpectedRecord вместе
// с реальным Encode/Decode (expected_pipeline_test.go): именно они
// покраснели на контрабанде. Порт на Java обязан повторить полное
// покрытие этими же случаями, а не полагаться на то, что у метода
// правильное число параметров.
//
// Не зависит от формата: работает от пары схем через matchStrategy, а
// не от строки "avro"/"protobuf"/"json-schema". Если какому-то формату
// понадобилось особое поведение сверх четырёх шагов ниже — это находка
// стенда, а не повод его подгонять.
//
// Четыре механических шага:
//  1. Переименование — поле читателя, найденное по alias'у писателя
//     (только Avro), меняет имя в выводе на имя читателя.
//  2. Отбрасывание — поле писателя без пары в схеме читателя в вывод
//     не попадает (реализовано тем, что вывод строится ПЕРЕБОРОМ ПОЛЕЙ
//     ЧИТАТЕЛЯ, а не писателя).
//  3. Добавление — поле читателя без пары у писателя получает default,
//     объявленный схемой читателя; если default не объявлен, но поле
//     ОБЯЗАТЕЛЬНО (Required) — нулевое значение типа; если поле НЕ
//     обязательно и default не объявлен — поле в ожидание не попадает
//     вовсе (круг 3: это отличие специфично для JSON Schema, где
//     "properties" и "required" — разные списки).
//  4. Приведение типа — если поле НАЙДЕНО ПО ТОЙ ЖЕ СУЩНОСТИ (то же
//     имя/alias, а для Protobuf — то же имя ПРИ совпавшем номере),
//     типы писателя и читателя расходятся — значение приводится к
//     типу читателя С СОХРАНЕНИЕМ СМЫСЛА (int 1 -> string "1"). Если
//     номер совпал, а ИМЯ — нет (переиспользование слота под другое
//     по смыслу поле, reuse_tag), это не тот же самый "предмет
//     разговора" — конвертация не производится вовсе, значение
//     переносится как есть.
func ExpectedRecord(rec map[string]any, writerSchema, readerSchema Schema) (map[string]any, error) {
	wFields, rFields, wStrategy, err := schemaPair(writerSchema, readerSchema)
	if err != nil {
		return nil, err
	}

	norm, _ := Normalize(rec).(map[string]any)

	// C1 (круг правок 3): запись обязана покрывать ВСЕ поля схемы
	// писателя. Раньше отсутствующее поле молча уходило в шаг 3
	// (добавить умолчание) вместо шага 4 (привести тип) — неполной
	// записью можно было добыть тот же нулевой результат, что раньше
	// добывали флагом --want. Отсутствие поля — ошибка ВВОДА, а не
	// повод достраивать ожидание.
	//
	// Круг правок 4 добавил к проверке ТИПЫ. Раньше сверялся только
	// набор имён: все поля на месте — идём дальше, а значение любого
	// типа под правильным именем проезжало прямиком в шаг 4
	// («привести тип»), который и был рычагом третьего обхода. Теперь
	// запись обязана соответствовать схеме писателя и по именам, и по
	// типам — и то, и другое проверяется ДО единого вызова кодека,
	// одинаково в обоих режимах пробы.
	for _, wf := range wFields {
		v, ok := norm[wf.Name]
		if !ok {
			return nil, fmt.Errorf(
				"expected: запись не содержит поле %q, объявленное схемой писателя %s — запись обязана покрывать все поля схемы писателя",
				wf.Name, writerSchema.Name)
		}
		// nil — законное значение для поля, допускающего отсутствие
		// (union с null у Avro): проверять его тип не по чему.
		if v == nil || wf.Kind == kindUnknown {
			continue
		}
		if vk := valueKind(v); vk != wf.Kind {
			return nil, fmt.Errorf(
				"expected: поле %q записи имеет тип %s, а схема писателя %s объявляет %s — запись обязана соответствовать схеме писателя и по типам",
				wf.Name, kindName(vk), writerSchema.Name, kindName(wf.Kind))
		}
		// Соответствие типу — это и разрядность тоже. Значение, которое
		// в объявленный тип не помещается, схеме НЕ соответствует, и
		// узнать об этом надо здесь, до кодирования: иначе кодек либо
		// усечёт его молча (как усекали мы), либо откажет — и то, и
		// другое припишут формату.
		if err := checkFitsDeclaredWidth(wf, v, writerSchema.Name); err != nil {
			return nil, err
		}
	}

	// Обратная сторона той же проверки (круг правок 7). Покрытие всех
	// полей схемы проверялось, а лишние ключи — нет, и запись с ключом,
	// которому не соответствует ни одно поле схемы писателя, доезжала
	// до кодирования. Контрольное плечо пишет такой ключ в байты и
	// читает обратно — «прочиталось, но не то» на НЕИЗМЕНЁННОЙ схеме;
	// json-schema отвергает запись при кодировании — «отказ формата»
	// там, где формат ни при чём. Это тот же класс, что разрядность:
	// порча наша, а предъявлена была бы формату.
	declared := make(map[string]bool, len(wFields))
	for _, wf := range wFields {
		declared[wf.Name] = true
	}
	for name := range norm {
		if !declared[name] {
			return nil, fmt.Errorf(
				"expected: запись содержит поле %q, которого нет в схеме писателя %s — запись обязана описываться схемой писателя целиком",
				name, writerSchema.Name)
		}
	}

	out := make(map[string]any, len(rFields))
	for _, rf := range rFields {
		wf, wv, found, sameIdentity := matchWriterField(rf, wFields, norm, wStrategy)
		switch {
		case found && sameIdentity:
			out[rf.Name] = convertValue(wv, wf.Kind, rf.Kind)
		case found:
			// matchByNumber нашёл физический слот, но это НЕ то же
			// поле по имени (reuse_tag: писатель писал "email", номер
			// переиспользован читателем под "login_count"). Это не
			// ретайп одного и того же поля — приводить тип было бы
			// категориальной ошибкой независимо от того, распарсится
			// ли КОНКРЕТНОЕ значение как число (иначе строка "0" в
			// email давала бы честный "ok" по чистой случайности —
			// живой обход, которым ревью доказало дыру круга 2).
			out[rf.Name] = wv
		case rf.HasDefault:
			out[rf.Name] = rf.Default
		case rf.Required:
			out[rf.Name] = zeroForKind(rf.Kind)
		}
		// Ни default, ни required: поле необязательно, писатель его
		// не прислал — оно не появится и в ожидании (шаг 3, нижняя
		// ветка для JSON Schema).
	}
	return out, nil
}

// schemaPair читает обе схемы и проверяет всё, что можно проверить БЕЗ
// записи: что нотация одна и та же и что заявленное изменение в ней
// вообще выражается.
func schemaPair(writerSchema, readerSchema Schema) (wFields, rFields []schemaField, strategy matchStrategy, err error) {
	wFields, wStrategy, err := schemaFieldsFor(writerSchema)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("expected: схема писателя %s: %w", writerSchema.Name, err)
	}
	rFields, rStrategy, err := schemaFieldsFor(readerSchema)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("expected: схема читателя %s: %w", readerSchema.Name, err)
	}
	if wStrategy != rStrategy {
		return nil, nil, 0, fmt.Errorf("expected: писатель (%s) и читатель (%s) заданы схемами разных нотаций — сравнивать нечего",
			writerSchema.Name, readerSchema.Name)
	}

	// Вырожденность проверяем ТОЛЬКО когда схемы разные. «Разные» —
	// это РАЗНЫЕ ЗАПИСИ МАНИФЕСТА, а не разное содержимое (круг 6):
	// у вырожденной пары содержимое как раз совпадает побайтово, и
	// прочтение «одинаковые байты — один файл» отключило бы проверку
	// ровно там, ради чего она заведена.
	if !SameSchema(writerSchema, readerSchema) && fieldsStructurallyEqual(wFields, rFields) {
		return nil, nil, 0, fmt.Errorf("%w (%s vs %s)", ErrDegenerateSchema, writerSchema.Name, readerSchema.Name)
	}
	return wFields, rFields, wStrategy, nil
}

// SchemaPreparer — плечо, которому есть что подготовить, прежде чем
// схема станет рабочей: разобрать, собрать дескриптор, скомпилировать.
// Плечо без такой подготовки (контрольное) интерфейс не реализует.
type SchemaPreparer interface {
	PrepareSchema(s Schema) error
}

// PrepareSchema приводит схему в рабочий вид ЗАРАНЕЕ — и для вычисления
// ожидания, и для самого плеча.
//
// Круг правок 7. Раньше подготовка случалась по дороге, и исход «сбой
// пробы» получался разным в зависимости от того, кто споткнулся первым:
// если схему не мог разобрать расчёт ожидания, проба падала целиком и
// не печатала ни строки; если её не мог скомпилировать кодек — строка
// печаталась с исходом error, но уже со стадией и с ожиданием. Вторая
// реализация, устроенная иначе, честно разошлась с первой в области,
// которую спека объявила обязательной.
//
// Теперь подготовка — отдельный шаг ДО первой записи, и её неудача
// делает исходом error всю клетку целиком, единообразно.
func PrepareSchema(c Codec, s Schema) error {
	if _, _, err := schemaFieldsFor(s); err != nil {
		return err
	}
	if p, ok := c.(SchemaPreparer); ok {
		return p.PrepareSchema(s)
	}
	return nil
}

// SchemasAreDegenerate отвечает на вопрос «выражается ли заявленное
// изменение в этой нотации» ОДНОЙ ПАРОЙ СХЕМ, без записи.
//
// Круг правок 4: вызывающей стороне это нужно знать ДО того, как она
// возьмётся за записи. Иначе вырожденная пара (reuse_tag у Avro, где
// схема читателя структурно совпадает со схемой писателя) спотыкалась
// бы о проверку соответствия записи схеме — и находка стенда
// («переиспользование НОМЕРА поля есть только у Protobuf») выглядела бы
// как его поломка.
//
// Возвращает ошибку, обёртывающую ErrDegenerateSchema, если пара
// вырождена; nil — если мерить есть что; любую другую ошибку — если
// схемы не читаются.
func SchemasAreDegenerate(writerSchema, readerSchema Schema) error {
	_, _, _, err := schemaPair(writerSchema, readerSchema)
	return err
}

// matchWriterField ищет в wFields поле, которое matchStrategy считает
// "тем же" полем, что rf, и возвращает его текущее значение из record
// писателя (record проиндексирована ИМЕНЕМ ПОЛЯ ПИСАТЕЛЯ — таково
// соглашение стенда: запись всегда имеет форму схемы писателя, см.
// круг правок 1 про records.json).
//
// sameIdentity отличает НАСТОЯЩЕЕ совпадение поля (та же сущность,
// пусть и под другим именем/типом) от случайного совпадения
// ФИЗИЧЕСКОГО слота (тот же номер, но другое имя — Protobuf,
// reuse_tag). Для matchByName/matchByNameOrAlias совпадение уже
// включает имя/alias по построению, поэтому там sameIdentity всегда
// true при found.
func matchWriterField(rf schemaField, wFields []schemaField, rec map[string]any, strategy matchStrategy) (wf schemaField, value any, found, sameIdentity bool) {
	for _, cand := range wFields {
		matched := false
		switch strategy {
		case matchByNumber:
			matched = cand.Number == rf.Number
		case matchByNameOrAlias:
			matched = cand.Name == rf.Name || containsString(rf.Aliases, cand.Name)
		default: // matchByName
			matched = cand.Name == rf.Name
		}
		if !matched {
			continue
		}
		v, ok := rec[cand.Name]
		if !ok {
			continue
		}
		same := strategy != matchByNumber || cand.Name == rf.Name
		return cand, v, true, same
	}
	return schemaField{}, nil, false, false
}

// convertValue — шаг 4. Вызывается ТОЛЬКО когда matchWriterField
// подтвердила sameIdentity (см. выше) — то есть когда речь идёт о
// настоящем ретайпе одного и того же поля, а не о столкновении на
// переиспользованном физическом слоте. Определено только для пар,
// которые реально встречаются в семи изменениях стенда (int<->string);
// остальное возвращается без изменений, потому что придумывать
// конвертацию заранее, не видя случая, — гадание, а не находка.
func convertValue(v any, from, to fieldKind) any {
	if from == to || v == nil {
		return v
	}
	switch {
	case from == kindInt && to == kindString:
		// Любое целое имеет каноничную десятичную запись — смысл
		// сохраняется полностью и обратимо. Это тот самый "1" -> "1".
		if i, ok := v.(int64); ok {
			return strconv.FormatInt(i, 10)
		}
		return v
	case from == kindString && to == kindInt:
		s, ok := v.(string)
		if !ok {
			return v
		}
		i, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			// Строка — не десятичное число: смысл сохранить нечем.
			// Возвращаем исходное значение НЕИЗМЕНЁННЫМ, а не 0.
			return v
		}
		return i
	default:
		return v
	}
}

// valueKind опознаёт тип уже НОРМАЛИЗОВАННОГО значения записи (см.
// Normalize): целые к этому моменту приведены к одному представлению,
// так что словарь типов стенда покрывает всё, что может встретиться в
// корректной записи. Всё остальное — kindUnknown, и это тоже ответ:
// значение, тип которого стенд не умеет назвать, схеме соответствовать
// не может.
func valueKind(v any) fieldKind {
	switch v.(type) {
	case int64:
		return kindInt
	case string:
		return kindString
	case bool:
		return kindBool
	default:
		return kindUnknown
	}
}

// checkFitsDeclaredWidth проверяет, что целое значение помещается в
// разрядность, объявленную схемой. Правило одно на все нотации:
// разрядность объявляет схема, а не язык реализации.
func checkFitsDeclaredWidth(f schemaField, v any, writerSchema string) error {
	if f.Kind != kindInt || f.Bits != 32 {
		return nil
	}
	i, ok := v.(int64)
	if !ok {
		return nil
	}
	if i < math.MinInt32 || i > math.MaxInt32 {
		return fmt.Errorf(
			"expected: значение %d поля %q не помещается в разрядность, объявленную схемой писателя %s (%d бита) — запись обязана соответствовать схеме писателя",
			i, f.Name, writerSchema, f.Bits)
	}
	return nil
}

func kindName(k fieldKind) string {
	switch k {
	case kindInt:
		return "целое"
	case kindString:
		return "строка"
	case kindBool:
		return "логическое"
	default:
		return "неизвестный стенду тип"
	}
}

func zeroForKind(k fieldKind) any {
	switch k {
	case kindInt:
		return int64(0)
	case kindString:
		return ""
	case kindBool:
		return false
	default:
		return nil
	}
}

func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// fieldsStructurallyEqual сравнивает два набора полей без учёта
// порядка. Совпадение здесь — единственный сигнал вырожденности,
// который ExpectedRecord умеет обнаружить сам, не зная имён изменений:
// если читатель, объявленный ДРУГИМ файлом, тем не менее описывает ТЕ
// ЖЕ поля — эта нотация не выражает заявленное изменение.
func fieldsStructurallyEqual(a, b []schemaField) bool {
	if len(a) != len(b) {
		return false
	}
	sig := func(fs []schemaField) []string {
		out := make([]string, len(fs))
		for i, f := range fs {
			aliases := append([]string(nil), f.Aliases...)
			sort.Strings(aliases)
			out[i] = fmt.Sprintf("%s|%d|%d|%v|%v|%v|%v", f.Name, f.Number, f.Kind, f.HasDefault, f.Default, f.Required, aliases)
		}
		sort.Strings(out)
		return out
	}
	as, bs := sig(a), sig(b)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// schemaFieldsFor разбирает поля схемы согласно её НОТАЦИИ.
//
// Круг правок 6: нотацию раньше выводили из расширения файла. Теперь
// она приходит из записи манифеста вместе с содержимым — того же
// источника, который выбрал схему по координатам клетки. Расхождения
// между «каким плечом кодируем» и «как разбираем схему» больше не
// существует: и то, и другое взято из одной координаты.
func schemaFieldsFor(schema Schema) ([]schemaField, matchStrategy, error) {
	switch schema.Notation {
	case NotationAvro:
		f, err := avroSchemaFields(schema)
		return f, matchByNameOrAlias, err
	case NotationProtobuf:
		f, err := protoSchemaFields(schema)
		return f, matchByNumber, err
	case NotationJSONSchema:
		f, err := jsonSchemaFieldsRaw(schema)
		return f, matchByName, err
	default:
		return nil, 0, probeFailuref("схема %s объявлена в неизвестной нотации %q", schema.Name, schema.Notation)
	}
}

func avroKind(s avro.Schema) fieldKind {
	if s.Type() == avro.Union {
		for _, t := range s.(*avro.UnionSchema).Types() {
			if t.Type() != avro.Null {
				return avroKind(t)
			}
		}
		return kindUnknown
	}
	switch s.Type() {
	case avro.Long, avro.Int:
		return kindInt
	case avro.String:
		return kindString
	case avro.Boolean:
		return kindBool
	default:
		return kindUnknown
	}
}

// avroBits — разрядность, объявленная схемой Avro: "int" — 32, "long" —
// 64. Объединение разворачивается до первой не-пустой ветки, как и при
// определении категории.
func avroBits(s avro.Schema) int {
	if s.Type() == avro.Union {
		for _, t := range s.(*avro.UnionSchema).Types() {
			if t.Type() != avro.Null {
				return avroBits(t)
			}
		}
		return 0
	}
	switch s.Type() {
	case avro.Int:
		return 32
	case avro.Long:
		return 64
	default:
		return 0
	}
}

func avroSchemaFields(schema Schema) ([]schemaField, error) {
	s, err := avro.Parse(string(schema.Bytes))
	if err != nil {
		return nil, probeFailure("разобрать схему Avro", err)
	}
	rec, ok := s.(*avro.RecordSchema)
	if !ok {
		return nil, probeFailuref("%s: верхний уровень схемы — не record", schema.Name)
	}
	out := make([]schemaField, 0, len(rec.Fields()))
	for _, f := range rec.Fields() {
		out = append(out, schemaField{
			Name:    f.Name(),
			Aliases: f.Aliases(),
			Kind:    avroKind(f.Type()),
			Bits:    avroBits(f.Type()),
			// В Avro у поля либо есть default, либо оно обязано быть
			// у любого корректного писателя — "необязательного без
			// default" тут не бывает (в отличие от JSON Schema).
			Required:   true,
			HasDefault: f.HasDefault(),
			Default:    normalizeDefault(f),
		})
	}
	return out, nil
}

// normalizeDefault приводит default-значение поля к тому же виду, что
// и остальной пакет (int64 вместо float64/int и т.п.) — сравнение
// ExpectedRecord с реальным Decode делается через Normalize с обеих
// сторон, но default читается один раз при загрузке схемы, и лучше
// нормализовать его тут же, чем полагаться, что вызывающий код не
// забудет сделать это снаружи.
func normalizeDefault(f *avro.Field) any {
	if !f.HasDefault() {
		return nil
	}
	return Normalize(f.Default())
}

func protoKind(k protoreflect.Kind) fieldKind {
	switch k {
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return kindInt
	case protoreflect.StringKind:
		return kindString
	case protoreflect.BoolKind:
		return kindBool
	default:
		return kindUnknown
	}
}

// protoBits — разрядность, объявленная схемой Protobuf.
func protoBits(k protoreflect.Kind) int {
	switch k {
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return 32
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return 64
	default:
		return 0
	}
}

func protoSchemaFields(schema Schema) ([]schemaField, error) {
	md, err := protoMessageDescriptor(schema)
	if err != nil {
		return nil, err
	}
	fds := md.Fields()
	out := make([]schemaField, 0, fds.Len())
	for i := 0; i < fds.Len(); i++ {
		fd := fds.Get(i)
		kind := protoKind(fd.Kind())
		out = append(out, schemaField{
			Name:   string(fd.Name()),
			Number: int32(fd.Number()),
			Kind:   kind,
			Bits:   protoBits(fd.Kind()),
			// proto3 не объявляет defaults явно, но семантика "нет
			// значения — нулевое значение типа" встроена в сам формат:
			// с точки зрения ExpectedRecord это неотличимо от
			// объявленного default'а, поэтому HasDefault всегда true
			// (а Required уже не участвует в выборе ветки).
			Required:   true,
			HasDefault: true,
			Default:    zeroForKind(kind),
		})
	}
	return out, nil
}

// rawJSONSchemaDoc — часть schemas/user_v1.json и user_v2_*.json,
// нужная ExpectedRecord: имена, типы и обязательность свойств.
// Читается НАПРЯМУЮ из файла, а не через скомпилированную
// *jsonschema.Schema (которая нужна jsonSchemaCodec для валидации, а
// не для перечисления полей) — два разных назначения, два разных
// способа прочитать один файл.
type rawJSONSchemaDoc struct {
	Properties map[string]struct {
		Type string `json:"type"`
	} `json:"properties"`
	// Required — круг правок 3: раньше HasDefault для JSON Schema было
	// всегда false, и шаг 3 БЕЗУСЛОВНО добавлял нулевое значение для
	// любого поля читателя без пары у писателя. Но "properties" в JSON
	// Schema — это "это поле МОЖЕТ быть", а не "оно ОБЯЗАНО быть";
	// обязательность — отдельный список "required". Необязательное
	// поле, которого писатель не прислал, не обязано появляться и в
	// ожидании — а прежний код считал иначе и превращал законный "ok"
	// в "wrong" (add_default.age, unknown_field.nickname — ни то, ни
	// другое не входит в "required" собственных схем).
	Required []string `json:"required"`
}

func jsonSchemaFieldsRaw(schema Schema) ([]schemaField, error) {
	var doc rawJSONSchemaDoc
	if err := json.Unmarshal(schema.Bytes, &doc); err != nil {
		return nil, probeFailure("разобрать схему JSON Schema", err)
	}
	required := make(map[string]bool, len(doc.Required))
	for _, name := range doc.Required {
		required[name] = true
	}
	out := make([]schemaField, 0, len(doc.Properties))
	for name, p := range doc.Properties {
		kind := jsonKind(p.Type)
		out = append(out, schemaField{
			Name: name,
			Kind: kind,
			// JSON Schema разрядность целого не объявляет вовсе
			// (объявила бы — через minimum/maximum, чего схемы стенда
			// не делают), поэтому ограничения по ширине здесь нет.
			Bits: 0,
			// Ни одна .json-схема стенда default не объявляет —
			// HasDefault всегда false, шаг 3 для JSON Schema всегда
			// смотрит на Required.
			HasDefault: false,
			Default:    nil,
			Required:   required[name],
		})
	}
	return out, nil
}

func jsonKind(t string) fieldKind {
	switch t {
	case "integer":
		return kindInt
	case "string":
		return kindString
	case "boolean":
		return kindBool
	default:
		return kindUnknown
	}
}
