package access

import (
	"reflect"
	"testing"
)

func TestFieldByName(t *testing.T) {
	u := User{ID: 1, Name: "Ада", Email: "ada@example.com"}
	v, ok := FieldByName(u, "Name")
	if !ok || v.(string) != "Ада" {
		t.Fatalf("Name = %v ok=%v", v, ok)
	}
	if _, ok := FieldByName(u, "Missing"); ok {
		t.Fatal("несуществующее поле не должно находиться")
	}
}

func TestJSONTags(t *testing.T) {
	got := JSONTags(User{})
	want := map[string]string{"ID": "id", "Name": "name", "Email": "email"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("теги: %v, ждали %v (admin без тега пропущен)", got, want)
	}
}

func TestSetStringOnPointer(t *testing.T) {
	u := &User{Name: "старое"}
	if err := SetString(u, "Name", "новое"); err != nil {
		t.Fatalf("SetString: %v", err)
	}
	if u.Name != "новое" {
		t.Fatalf("поле не изменилось: %q", u.Name)
	}
}

func TestSetStringRequiresPointer(t *testing.T) {
	// значение (не указатель) не адресуемо — reflect не даст менять
	if err := SetString(User{}, "Name", "x"); err == nil {
		t.Fatal("ждали ошибку: значение не адресуемо, нужен указатель")
	}
}

func TestSetStringUnexportedFails(t *testing.T) {
	// неэкспортируемое поле admin нельзя менять через reflect
	if err := SetString(&User{}, "admin", "x"); err == nil {
		t.Fatal("ждали ошибку: неэкспортируемое поле неизменяемо")
	}
}

func TestDescribe(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{42, "целое 42"},
		{"hi", `строка "hi"`},
		{[]int{1, 2, 3}, "последовательность из 3 элементов"},
		{User{ID: 1}, "структура User с 4 полями"},
	}
	for _, c := range cases {
		if got := Describe(c.in); got != c.want {
			t.Errorf("Describe(%v) = %q, ждали %q", c.in, got, c.want)
		}
	}
}
