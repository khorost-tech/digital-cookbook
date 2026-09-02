package codec

import "testing"

func TestAvroCodecRoundTripSameSchema(t *testing.T) {
	c, err := New("avro")
	if err != nil {
		t.Fatalf("New(avro): %v", err)
	}
	v1 := schemaPath(t, "user_v1.avsc")
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

// add_default: у писателя v1 поля age нет, у читателя v2 оно union
// ["null","int"] с default null — Resolve обязан заполнить его
// значением по умолчанию, а не отказать.
func TestAvroCodecAddDefaultNewerReaderOK(t *testing.T) {
	c, err := New("avro")
	if err != nil {
		t.Fatalf("New(avro): %v", err)
	}
	v1 := schemaPath(t, "user_v1.avsc")
	v2 := schemaPath(t, "user_v2_add_default.avsc")
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}

	b, err := c.Encode(rec, v1)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := c.Decode(b, v1, v2)
	if err != nil {
		t.Fatalf("Decode (writer=v1, reader=v2_add_default): %v", err)
	}
	if got["age"] != nil {
		t.Fatalf("ожидали default null для age, получили %#v", got["age"])
	}
}

// add_nodefault: у читателя v2 поле age без default — Resolve обязан
// отказать, потому что заполнить нечем.
func TestAvroCodecAddNoDefaultNewerReaderRefused(t *testing.T) {
	c, err := New("avro")
	if err != nil {
		t.Fatalf("New(avro): %v", err)
	}
	v1 := schemaPath(t, "user_v1.avsc")
	v2 := schemaPath(t, "user_v2_add_nodefault.avsc")
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}

	b, err := c.Encode(rec, v1)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if _, err := c.Decode(b, v1, v2); err == nil {
		t.Fatal("ожидали отказ: у читателя обязательное поле age без default, у писателя его нет")
	}
}

// rename: читатель v2 объявляет alias "email" для своего поля
// "contact" — писатель v1 писал "email", и это должно разрешиться.
func TestAvroCodecRenameResolvesByAlias(t *testing.T) {
	c, err := New("avro")
	if err != nil {
		t.Fatalf("New(avro): %v", err)
	}
	v1 := schemaPath(t, "user_v1.avsc")
	v2 := schemaPath(t, "user_v2_rename.avsc")
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
		t.Fatalf("alias не сработал: got %#v", got)
	}
}

// retype: id меняет тип long -> string при одном номере поля — схемы
// несовместимы по правилам Avro (типы не promotable в обе стороны),
// значит Resolve обязан отказать заранее, а не отдать мусор.
func TestAvroCodecRetypeRefused(t *testing.T) {
	c, err := New("avro")
	if err != nil {
		t.Fatalf("New(avro): %v", err)
	}
	v1 := schemaPath(t, "user_v1.avsc")
	v2 := schemaPath(t, "user_v2_retype.avsc")
	rec := map[string]any{"id": int64(1), "name": "Анна", "email": "anna@example.com"}

	b, err := c.Encode(rec, v1)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if _, err := c.Decode(b, v1, v2); err == nil {
		t.Fatal("ожидали отказ на несовместимой смене типа id long -> string")
	}
}
