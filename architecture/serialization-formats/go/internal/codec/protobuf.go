package codec

import (
	"fmt"
	"math"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// userMessageName — полное имя сообщения во всех .proto стенда
// (schemas/user_v1.proto и семь user_v2_*.proto). Раз оно везде одно и
// то же, кодек не обязан знать имя сообщения снаружи — путь к .desc
// однозначно задаёт схему.
const userMessageName = protoreflect.FullName("tech.khorost.serialization.User")

// State — непрозрачный носитель того, что Decode не смог выразить в
// map[string]any. Тип объявлен на уровне пакета, а не внутри
// protobuf.go, потому что это часть публичного контракта codec, а не
// деталь одной реализации — хоть сегодня его производит и потребляет
// только protobufCodec.
type State any

// RoundTripper — опциональное расширение Codec для форматов, которые
// физически способны пронести неизвестные читателю данные через
// повторное кодирование. Это правка контракта пакета (круг ревью 1,
// находка C3), сделанная нарочно ДО Задачи 4: Java обязана реализовать
// тот же интерфейс для protobuf, иначе два языка будут измерять разные
// вещи под одним и тем же названием "сохранение неизвестных полей".
//
// Формат без такого свойства (json, json-schema, avro — там
// "неизвестное" либо не бывает, либо не переживает декодирование в
// принципе) интерфейс просто не реализует; вызывающая сторона узнаёт
// об этом через неудавшееся приведение типа, а не через ошибку
// заглушки.
type RoundTripper interface {
	// DecodeState — как Decode, но дополнительно возвращает State:
	// непрозрачные данные, которые readerSchema не знает, но которые
	// не должны потеряться при последующем EncodeState.
	DecodeState(b []byte, writer, reader Schema) (map[string]any, State, error)
	// EncodeState — как Encode, но переносит вперёд State, полученный
	// от DecodeState: то, что было неизвестно читателю на декодировании,
	// обязано пережить повторное кодирование — той же или другой schema.
	EncodeState(rec map[string]any, schema Schema, state State) ([]byte, error)
}

// protobufCodec работает через дескрипторы и динамические сообщения
// (google.golang.org/protobuf/reflect/protodesc + types/dynamicpb), а
// НЕ через сгенерированный код. Это решение контроллера, а не
// оптимизация: Java-часть (Задача 4) обязана пойти тем же путём, иначе
// языки будут сравнивать разные механизмы, а не разные форматы. Заодно
// это единственный способ честно измерить размер дескриптора и
// гарантированно сохранить неизвестные поля при декодировании — со
// сгенерированным кодом под конкретную версию схемы это было бы
// незаметно подделано самой генерацией.
//
// Схема (аргумент Encode/Decode) — путь к .desc-файлу
// (google.protobuf.FileDescriptorSet), собранному
// schemas/build-descriptors.sh через buf build --as-file-descriptor-set.
type protobufCodec struct {
	mu    sync.Mutex
	cache map[string]protoreflect.MessageDescriptor
}

func newProtobufCodec() *protobufCodec {
	return &protobufCodec{cache: map[string]protoreflect.MessageDescriptor{}}
}

// marshalDeterministic сериализует dynamicpb-сообщение С ФИКСИРОВАННЫМ
// порядком полей (по возрастанию номера).
//
// Круг ревью 2, находка C3: обычный proto.Marshal(msg) не гарантирует
// порядок обхода полей у *dynamicpb.Message — длина результата при этом
// стабильна (те же поля, те же значения), а порядок байт на проводе
// каждый раз может быть другим. Для схемы стенда это незаметно на
// bytes (сравнивают длину), но ломает сравнение содержимого пачки
// (batch-хеш, §M1) и портит zstd на пачке: восемь прогонов одной и той
// же сборки дали разброс 141-148, а закоммиченное число не совпало ни
// с одним из них — воспроизводимости не было вовсе, хотя выглядело,
// что есть. proto.MarshalOptions{Deterministic: true} фиксирует порядок
// известных полей; это НЕ канонический wire-формат Protobuf в общем
// смысле (спецификация Protobuf не гарантирует порядок обхода полей
// вообще, это библиотечная гарантия google.golang.org/protobuf), но
// делает ЭТУ пробу воспроизводимой — а без воспроизводимости byte-level
// сравнение бессмысленно независимо от формата.
func marshalDeterministic(msg *dynamicpb.Message) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(msg)
}

var (
	_ Codec        = (*protobufCodec)(nil)
	_ RoundTripper = (*protobufCodec)(nil)
)

// Ключ кэша — имя записи манифеста, оно же тождество схемы.
func (c *protobufCodec) load(schema Schema) (protoreflect.MessageDescriptor, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if md, ok := c.cache[schema.Name]; ok {
		return md, nil
	}
	md, err := protoMessageDescriptor(schema)
	if err != nil {
		return nil, err
	}
	c.cache[schema.Name] = md
	return md, nil
}

// protoMessageDescriptor — свободная функция (не метод), потому что у
// неё два потребителя: сам кодек и вычисление ожидания, которое читает
// те же схемы и не обязано заводить ради этого свой кэш.
//
// Круг правок 6: дескриптор собирается из УЖЕ прочитанных байтов.
// Каждая неудача ниже — сбой пробы, а не отказ формата: дескриптор ещё
// не собран, Protobuf в этот момент ничего не решал.
func protoMessageDescriptor(schema Schema) (protoreflect.MessageDescriptor, error) {
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(schema.Bytes, &set); err != nil {
		return nil, probeFailure("содержимое не является FileDescriptorSet", err)
	}
	files, err := protodesc.NewFiles(&set)
	if err != nil {
		return nil, probeFailure("собрать файлы дескриптора", err)
	}
	d, err := files.FindDescriptorByName(userMessageName)
	if err != nil {
		return nil, probeFailure(fmt.Sprintf("сообщение %s не найдено в %s", userMessageName, schema.Name), err)
	}
	md, ok := d.(protoreflect.MessageDescriptor)
	if !ok {
		return nil, probeFailuref("%s в %s — не сообщение", userMessageName, schema.Name)
	}
	return md, nil
}

// buildMessage собирает *dynamicpb.Message по схеме и generic-записи.
// Используется и Encode, и EncodeState — единственная разница между
// ними в том, дописывает ли вызывающий код к результату unknown fields
// предыдущего сообщения (см. EncodeState).
func (c *protobufCodec) buildMessage(rec map[string]any, schema Schema) (*dynamicpb.Message, error) {
	md, err := c.load(schema)
	if err != nil {
		return nil, err
	}
	msg := dynamicpb.NewMessage(md)
	norm, _ := Normalize(rec).(map[string]any)
	fields := md.Fields()
	for k, v := range norm {
		fd := fields.ByName(protoreflect.Name(k))
		if fd == nil {
			// Ключ, неизвестный схеме писателя — не ошибка вызывающего
			// кода: одна и та же generic-запись используется во всех
			// плечах, а схемы версий отличаются набором полей.
			continue
		}
		val, err := toProtoValue(fd, v)
		if err != nil {
			return nil, fmt.Errorf("поле %s: %w", k, err)
		}
		msg.Set(fd, val)
	}
	return msg, nil
}

// messageToMap читает ТОЛЬКО известные readerSchema поля — то, что ей
// неизвестно, в map попасть не может по определению map[string]any.
// Именно поэтому неизвестные данные носит State, а не эта функция
// (находка C3: раньше unknown fields честно сохранялись в сообщении,
// но тут же терялись, потому что наружу отдавалась только map).
//
// Принимает protoreflect.Message (интерфейс), а не конкретный
// *dynamicpb.Message: круг правок (retype_message, задача 6bis) —
// вложенное сообщение, полученное через fromProtoValue у поля типа
// MessageKind, снаружи выглядит как protoreflect.Message того же
// интерфейса, и messageToMap рекурсивно раскрывает его в map тем же
// кодом, каким читает верхний уровень. *dynamicpb.Message этому
// интерфейсу удовлетворяет, так что вызывающий код (DecodeState) не
// меняется.
func messageToMap(md protoreflect.MessageDescriptor, msg protoreflect.Message) map[string]any {
	fields := md.Fields()
	out := make(map[string]any, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		// Get(), а не Range(): proto3-поля без "optional" не отслеживают
		// presence, и запись про них должна быть видна в результате
		// даже если значение — ноль (тот самый случай reuse_tag, где
		// "молчаливо не то" — это и есть находка стенда).
		out[string(fd.Name())] = fromProtoValue(fd, msg.Get(fd))
	}
	return out
}

// PrepareSchema — см. SchemaPreparer.
func (c *protobufCodec) PrepareSchema(schema Schema) error {
	_, err := c.load(schema)
	return err
}

func (c *protobufCodec) Encode(rec map[string]any, schema Schema) ([]byte, error) {
	msg, err := c.buildMessage(rec, schema)
	if err != nil {
		return nil, fmt.Errorf("protobuf: схема писателя %s: %w", schema.Name, err)
	}
	return marshalDeterministic(msg)
}

func (c *protobufCodec) Decode(b []byte, writer, reader Schema) (map[string]any, error) {
	m, _, err := c.DecodeState(b, writer, reader)
	return m, err
}

// DecodeState — см. RoundTripper. State здесь — сам *dynamicpb.Message:
// после proto.Unmarshal он уже несёт неизвестные читателю байты в
// своих unknown fields (это встроенное поведение библиотеки, decode не
// делает для этого ничего специального), достаточно не выбросить
// сообщение целиком, оставив снаружи одну map.
func (c *protobufCodec) DecodeState(b []byte, _, reader Schema) (map[string]any, State, error) {
	md, err := c.load(reader)
	if err != nil {
		return nil, nil, fmt.Errorf("protobuf: схема читателя %s: %w", reader.Name, err)
	}
	msg := dynamicpb.NewMessage(md)
	// Явно НЕ трогаем UnmarshalOptions.DiscardUnknown (по умолчанию
	// false) — неизвестные полю читателя данные обязаны остаться в
	// unknown fields сообщения, это измеряемое свойство протобуфа, а
	// не мусор, от которого можно избавиться.
	if err := proto.Unmarshal(b, msg); err != nil {
		return nil, nil, fmt.Errorf("protobuf: декодирование: %w", err)
	}
	out, _ := Normalize(messageToMap(md, msg)).(map[string]any)
	return out, State(msg), nil
}

// EncodeState — см. RoundTripper. Строит НОВОЕ сообщение по (rec,
// schema), как обычный Encode, а затем дописывает к нему unknown
// fields из state — если он есть.
//
// nil state — легальный, документированный случай (EncodeState ведёт
// себя как обычный Encode: не у каждого вызова есть предыдущее
// состояние). А вот НЕ-nil state неопознанного типа — это, круг правок
// 2, находка "мелочь 3": раньше он тоже молча принимался и просто не
// давал эффекта, то есть Java, перепутавшая state одного формата с
// другим (или передавшая что-то своё), получила бы зелёный тест по
// ложной причине — EncodeState тихо выполнил бы Encode, а вызывающий
// код решил бы, что unknown fields перенеслись. Теперь это explicit
// ошибка.
func (c *protobufCodec) EncodeState(rec map[string]any, schema Schema, state State) ([]byte, error) {
	msg, err := c.buildMessage(rec, schema)
	if err != nil {
		return nil, fmt.Errorf("protobuf: схема писателя %s: %w", schema.Name, err)
	}
	if state != nil {
		prev, ok := state.(*dynamicpb.Message)
		if !ok {
			return nil, fmt.Errorf("protobuf: EncodeState получил state неизвестного типа %T — ожидался *dynamicpb.Message из DecodeState этого же кодека", state)
		}
		if unk := prev.GetUnknown(); len(unk) > 0 {
			msg.SetUnknown(append(msg.GetUnknown(), unk...))
		}
	}
	return marshalDeterministic(msg)
}

// toProtoValue конвертирует значение generic-записи в protoreflect.Value
// по Kind поля. Схемы стенда используют только int64/int32/string —
// остального осознанно не поддерживаем, чтобы неожиданный тип поля в
// новой версии схемы падал явной ошибкой, а не молчал.
func toProtoValue(fd protoreflect.FieldDescriptor, v any) (protoreflect.Value, error) {
	switch fd.Kind() {
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		i, ok := v.(int64)
		if !ok {
			return protoreflect.Value{}, fmt.Errorf("ожидали целое, получили %T", v)
		}
		return protoreflect.ValueOfInt64(i), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		i, ok := v.(int64)
		if !ok {
			return protoreflect.Value{}, fmt.Errorf("ожидали целое, получили %T", v)
		}
		// Круг правок 5: здесь стояло голое int32(i), и оно молча
		// усекало. Живая проба, где схема писателя и читателя ОДНА И ТА
		// ЖЕ, показывала из-за этого «прочиталось, но не то» — то есть
		// нашу порчу под видом поведения Protobuf. У Avro такое же
		// усечение заменено ошибкой кругом раньше; здесь сделано
		// симметрично.
		if i < math.MinInt32 || i > math.MaxInt32 {
			return protoreflect.Value{}, probeFailuref(
				"значение %d не помещается в объявленный схемой тип int32", i)
		}
		return protoreflect.ValueOfInt32(int32(i)), nil
	case protoreflect.StringKind:
		s, ok := v.(string)
		if !ok {
			return protoreflect.Value{}, fmt.Errorf("ожидали строку, получили %T", v)
		}
		return protoreflect.ValueOfString(s), nil
	case protoreflect.BoolKind:
		bo, ok := v.(bool)
		if !ok {
			return protoreflect.Value{}, fmt.Errorf("ожидали bool, получили %T", v)
		}
		return protoreflect.ValueOfBool(bo), nil
	case protoreflect.MessageKind:
		// retype_message (задача 6bis): поле сменило тип со строки на
		// вложенное сообщение. Значение канонической записи для ТАКОГО
		// поля стенд представляет обычной map[string]any (как и
		// верхний уровень записи) — это единственная запись писателя,
		// где такое поле вообще присутствует по-настоящему (records.json,
		// v2.retype_message), остальные клетки этого поля не пишут.
		m, ok := v.(map[string]any)
		if !ok {
			return protoreflect.Value{}, fmt.Errorf("ожидали вложенное сообщение (map), получили %T", v)
		}
		sub := dynamicpb.NewMessage(fd.Message())
		subFields := fd.Message().Fields()
		for k, vv := range m {
			sfd := subFields.ByName(protoreflect.Name(k))
			if sfd == nil {
				continue
			}
			sval, err := toProtoValue(sfd, vv)
			if err != nil {
				return protoreflect.Value{}, fmt.Errorf("вложенное поле %s: %w", k, err)
			}
			sub.Set(sfd, sval)
		}
		return protoreflect.ValueOfMessage(sub), nil
	default:
		return protoreflect.Value{}, fmt.Errorf("неподдерживаемый тип поля %s", fd.Kind())
	}
}

// fromProtoValue — обратное преобразование. protoreflect.Value.Int()
// возвращает int64 и для Int32Kind, и для Int64Kind — приводить типы
// вручную не нужно, Normalize() (см. normalize.go) сделает это в любом
// случае.
func fromProtoValue(fd protoreflect.FieldDescriptor, v protoreflect.Value) any {
	switch fd.Kind() {
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return v.Int()
	case protoreflect.StringKind:
		return v.String()
	case protoreflect.BoolKind:
		return v.Bool()
	case protoreflect.MessageKind:
		// retype_message: значение поля-сообщения раскрывается в
		// map[string]any рекурсивно — той же messageToMap, что и
		// верхний уровень. Без этого сюда попал бы протообразный
		// v.Interface() (protoreflect.Message), который RecordsEqual
		// не умеет сравнивать со значением "want" (обычной map) и
		// который небезопасно отдавать в JSON-энкодер строки результата
		// как есть.
		return messageToMap(fd.Message(), v.Message())
	default:
		return v.Interface()
	}
}
