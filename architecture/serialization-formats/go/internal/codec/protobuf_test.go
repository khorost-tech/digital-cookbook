package codec

import "testing"

// descPath возвращает .desc-схему, собранную
// schemas/build-descriptors.sh из соответствующего .proto через buf,
// уже прочитанной (круг правок 6: кодек принимает содержимое).
func descPath(t *testing.T, name string) Schema {
	t.Helper()
	return schemaPath(t, name)
}

func TestProtobufCodecRoundTripSameSchema(t *testing.T) {
	c, err := New("protobuf")
	if err != nil {
		t.Fatalf("New(protobuf): %v", err)
	}
	v1 := descPath(t, "user_v1.desc")
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}

	b, err := c.Encode(rec, v1)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := c.Decode(b, v1, v1)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	for k, v := range rec {
		if got[k] != v {
			t.Fatalf("поле %s: got %#v, want %#v", k, got[k], v)
		}
	}
}

// rename: proto держится на номерах полей, а не на именах — старые
// байты (writer v1, поле 3 "email") читаются новой схемой (field 3
// теперь называется "contact") без единой ошибки, просто под другим
// ключом.
func TestProtobufCodecRenameSurvivesByFieldNumber(t *testing.T) {
	c, err := New("protobuf")
	if err != nil {
		t.Fatalf("New(protobuf): %v", err)
	}
	v1 := descPath(t, "user_v1.desc")
	v2 := descPath(t, "user_v2_rename.desc")
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}

	b, err := c.Encode(rec, v1)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := c.Decode(b, v1, v2)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got["contact"] != "anna@example.com" {
		t.Fatalf("got %#v", got)
	}
}

// unknown_field: писатель v2 добавил nickname, читатель v1 о нём не
// знает — proto3 обязан промолчать (поле уходит в unknown fields), а
// не отказать.
func TestProtobufCodecUnknownFieldDoesNotError(t *testing.T) {
	c, err := New("protobuf")
	if err != nil {
		t.Fatalf("New(protobuf): %v", err)
	}
	v1 := descPath(t, "user_v1.desc")
	v2 := descPath(t, "user_v2_unknown_field.desc")
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com", "nickname": "anya"}

	b, err := c.Encode(rec, v2)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := c.Decode(b, v2, v1)
	if err != nil {
		t.Fatalf("Decode не должен отказывать на неизвестном поле: %v", err)
	}
	if got["email"] != "anna@example.com" {
		t.Fatalf("got %#v", got)
	}
	if _, ok := got["nickname"]; ok {
		t.Fatalf("читатель v1 не знает про nickname, полю неоткуда взяться в map: got %#v", got)
	}
}

// retype: id меняет wire-тип (int64 varint -> string length-delimited)
// при сохранении номера поля 1. Найдено вживую (см. отчёт, круг правок
// 1): decode НЕ отказывает — varint-байты не образуют валидную
// UTF-8-строку ожидаемой длины, и protoreflect отдаёт пустую строку.
// Раньше тест проходил при ЛЮБОЙ ошибке и ЛЮБОМ неверном значении
// (I3 ревью) — это давало зелёный тест даже если поведение сменится на
// правдоподобный мусор вместо пустой строки. Прибиваем гвоздём то, что
// реально наблюдается.
func TestProtobufCodecRetypeGivesEmptyStringNotError(t *testing.T) {
	c, err := New("protobuf")
	if err != nil {
		t.Fatalf("New(protobuf): %v", err)
	}
	v1 := descPath(t, "user_v1.desc")
	v2 := descPath(t, "user_v2_retype.desc")
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}

	b, err := c.Encode(rec, v1)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, decErr := c.Decode(b, v1, v2)
	if decErr != nil {
		t.Fatalf("ожидали тихий decode без ошибки, получили: %v", decErr)
	}
	if got["id"] != "" {
		t.Fatalf("ожидали id=\"\" (varint не парсится как валидная строка), получили %#v", got["id"])
	}
}

// reuse_tag: номер 3 после remove(email) отдан новому полю
// login_count (int32). Найдено вживую: protobuf-go сверяет wire type
// байтов с тем, что заявлен в схеме читателя, и при несовпадении не
// подсовывает мусор в известное поле — трактует занятые байты как
// unknown и оставляет login_count нулём по умолчанию. Это ВСЁ РАВНО
// "wrong" в терминах стенда: читатель уверен, что login_count==0
// ("ноль входов"), хотя на деле по этому номеру поля лежал e-mail —
// семантическая порча без единой ошибки и без "явного" мусора в виде
// case I3. Раньше эта клетка не была покрыта ни одним тестом (I4).
func TestProtobufCodecReuseTagGivesZeroNotError(t *testing.T) {
	c, err := New("protobuf")
	if err != nil {
		t.Fatalf("New(protobuf): %v", err)
	}
	v1 := descPath(t, "user_v1.desc")
	v2 := descPath(t, "user_v2_reuse_tag.desc")
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}

	b, err := c.Encode(rec, v1)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, decErr := c.Decode(b, v1, v2)
	if decErr != nil {
		t.Fatalf("ожидали тихий decode без ошибки, получили: %v", decErr)
	}
	if got["login_count"] != int64(0) {
		t.Fatalf("ожидали login_count=0 (wire type не совпал, поле не разобралось), получили %#v", got["login_count"])
	}
}

// C3 ревью: писатель v2 → читатель v1 (nickname неизвестен) → ОБРАТНОЕ
// кодирование схемой v1 → читатель v2 обязан снова увидеть nickname.
// До этой правки Decode отдавал только map[string]any, собранную
// перебором полей readerSchema — unknown fields читались библиотекой
// честно, но тут же выбрасывались вместе с сообщением. Единственный
// прежний тест на эту тему проверял ОТСУТСТВИЕ nickname в map — а это
// проходит одинаково что при сохранении, что при потере поля. Здесь
// проверяется утверждение по существу: значение переживает полный
// цикл decode→encode на схеме, которая о нём не знает.
func TestProtobufCodecUnknownFieldSurvivesRoundTrip(t *testing.T) {
	c, err := New("protobuf")
	if err != nil {
		t.Fatalf("New(protobuf): %v", err)
	}
	rt, ok := c.(RoundTripper)
	if !ok {
		t.Fatal("protobufCodec обязан реализовывать RoundTripper")
	}
	v1 := descPath(t, "user_v1.desc")
	v2 := descPath(t, "user_v2_unknown_field.desc")
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com", "nickname": "anya"}

	// Пишет писатель v2 (знает nickname).
	b, err := c.Encode(rec, v2)
	if err != nil {
		t.Fatalf("Encode(v2): %v", err)
	}

	// Читает читатель v1 (nickname не знает) — но забирает ещё и state.
	got, state, err := rt.DecodeState(b, v2, v1)
	if err != nil {
		t.Fatalf("DecodeState(v1): %v", err)
	}
	if _, ok := got["nickname"]; ok {
		t.Fatalf("читатель v1 не знает про nickname, в map его быть не должно: got %#v", got)
	}

	// Обратное кодирование ИМЕННО схемой читателя v1 — она тоже не
	// знает про nickname, и если бы EncodeState полагался на схему,
	// а не на state, поле потерялось бы здесь.
	b2, err := rt.EncodeState(got, v1, state)
	if err != nil {
		t.Fatalf("EncodeState(v1): %v", err)
	}

	// Раскодируем заново схемой v2 — nickname обязан оказаться на месте.
	got2, err := c.Decode(b2, v1, v2)
	if err != nil {
		t.Fatalf("Decode(v2) после кругового прогона: %v", err)
	}
	if got2["nickname"] != "anya" {
		t.Fatalf("nickname не пережил круговой прогон: got %#v", got2)
	}
	// Остальные поля v1 обязаны остаться корректными — круговой прогон
	// не должен портить то, что и так было известно обеим схемам.
	if got2["id"] != int64(1) || got2["name"] != "Анна" || got2["email"] != "anna@example.com" {
		t.Fatalf("известные поля пострадали при круговом прогоне: got %#v", got2)
	}
}

// Круг правок 2, "мелочь 3": EncodeState с state неопознанного типа
// раньше молча вела себя как обычный Encode — вызывающий код (Java,
// перепутавшая свой State с чужим, или тест, забывший про DecodeState)
// получил бы зелёный прогон по неверной причине. nil остаётся законным
// (EncodeState без предыдущего состояния — это и есть Encode), а вот
// не-nil мусор — explicit ошибка.
func TestProtobufCodecEncodeStateRejectsForeignState(t *testing.T) {
	c, err := New("protobuf")
	if err != nil {
		t.Fatalf("New(protobuf): %v", err)
	}
	rt, ok := c.(RoundTripper)
	if !ok {
		t.Fatal("protobufCodec обязан реализовывать RoundTripper")
	}
	v1 := descPath(t, "user_v1.desc")
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}

	if _, err := rt.EncodeState(rec, v1, "чужое состояние — не *dynamicpb.Message"); err == nil {
		t.Fatal("ожидали ошибку: state неопознанного типа")
	}

	// nil — по-прежнему легален и работает как обычный Encode.
	if _, err := rt.EncodeState(rec, v1, nil); err != nil {
		t.Fatalf("EncodeState с nil state не должен отказывать: %v", err)
	}
}
