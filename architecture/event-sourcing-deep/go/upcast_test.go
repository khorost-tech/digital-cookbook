package main

import "testing"

// Событие v1 не знает единиц измерения; v2 добавляет поле unit; v3
// переименовывает liters -> amount. Читатель обязан получить v3 из любой версии.
func TestUpcastChainV1ToV3(t *testing.T) {
	raw := []byte(`{"type":"OxygenAdded","liters":42}`)

	e, err := Upcast(raw, 1)
	if err != nil {
		t.Fatalf("upcast v1: %v", err)
	}
	if e.Amount != 42 {
		t.Fatalf("значение потеряно при upcast: %v", e.Amount)
	}
	if e.Unit != "liters" {
		t.Fatalf("v2 обязан подставить единицу по умолчанию, получено %q", e.Unit)
	}
}

// TestUpcastV2PreservesExistingUnit закрывает дыру, которую полевое
// постусловие FIXTURES не ловило: свёртка по Amount не заметила бы, если
// бы шаг v1->v2 перетирал уже существующий Unit чем-то другим. Здесь вход
// УЖЕ v2 (schema_ver=2, поле unit присутствует и НЕ равно "liters") — цикл
// цепочки стартует с v=2, шаг 1->2 не выполняется вовсе, выполняется только
// шаг 2->3, трогающий liters/amount. Если бы кто-то по ошибке вынес
// m["unit"]="liters" из шага 1 в общий код, этот тест бы упал.
func TestUpcastV2PreservesExistingUnit(t *testing.T) {
	raw := []byte(`{"type":"OxygenAdded","liters":10,"unit":"gallons"}`)

	e, err := Upcast(raw, 2)
	if err != nil {
		t.Fatalf("upcast v2: %v", err)
	}
	if e.Unit != "gallons" {
		t.Fatalf("шаг v1->v2 перетёр уже существующий unit: получено %q, ожидалось %q", e.Unit, "gallons")
	}
	if e.Amount != 10 {
		t.Fatalf("значение потеряно при upcast v2->v3: %v", e.Amount)
	}
}

// TestUpcastChainIsComplete — проверка на дыру в цепочке. Полнота цепочки
// раньше держалась только на том, что автор не забудет: currentSchemaVer и
// набор шагов — две независимые величины, и поднять одну без другой ничто
// не мешало. Событие старой версии тогда прошло бы часть пути и вышло в
// форме, которую код домена считает текущей, ею не будучи.
//
// Проверено падающим: при currentSchemaVer=4 без шага 3 тест сообщает
// «в цепочке нет шага v3->v4 (currentSchemaVer=4)».
func TestUpcastChainIsComplete(t *testing.T) {
	for v := 1; v < currentSchemaVer; v++ {
		if _, ok := upcastSteps[v]; !ok {
			t.Fatalf("в цепочке нет шага v%d->v%d (currentSchemaVer=%d): "+
				"версию подняли, шаг добавить забыли", v, v+1, currentSchemaVer)
		}
	}
}

// TestUpcastRejectsInvalidSchemaVer — нижняя граница диапазона. Прежняя
// ветка `case schemaVer <= 1` принимала 0 и отрицательные значения как v1,
// то есть строка с отсутствующим или битым schema_ver молча проходила всю
// цепочку, будто это осмысленная старая форма. Отсутствие данных не то же
// самое, что самая старая версия, и различать их обязан читатель.
func TestUpcastRejectsInvalidSchemaVer(t *testing.T) {
	raw := []byte(`{"type":"OxygenAdded","liters":42}`)

	for _, ver := range []int{0, -1, -42} {
		if _, err := Upcast(raw, ver); err == nil {
			t.Fatalf("schemaVer=%d — недопустимая версия, ожидалась ошибка, получен тихий проход", ver)
		}
	}
}

// TestUpcastRejectsFutureSchema проверяет верхнюю границу: событие со схемой
// новее, чем знает код (здесь — 4, код знает только 1-3), обязано вернуть
// явную ошибку. Тихий проход означал бы, что событие из будущего молча
// превращается в мусор (нулевые Amount/Unit) вместо явного отказа читателя.
func TestUpcastRejectsFutureSchema(t *testing.T) {
	raw := []byte(`{"type":"OxygenAdded","amount":5,"unit":"liters"}`)

	_, err := Upcast(raw, currentSchemaVer+1)
	if err == nil {
		t.Fatalf("схема версии %d новее кода (знает до %d) — ожидалась ошибка, получен тихий проход",
			currentSchemaVer+1, currentSchemaVer)
	}
}

// benchRaw — ОДНИ И ТЕ ЖЕ сырые байты для обоих бенчмарков ниже. Это
// намеренно: полевой замер (04-versioning.sh) сравнивает чтение РЕАЛЬНОГО
// v1-стрима (короче: 2 поля) против РЕАЛЬНОГО v3-стрима (длиннее: 3 поля) и
// НЕ показал устойчивого направления на чтении — отношение v1/v3 upcast_ms
// колеблется вокруг 1.0 и переходит через неё по раундам (см. FIXTURES.md,
// «Артефакт 04»). Это путает две причины: длину JSON и число шагов цепочки.
// Здесь длина JSON зафиксирована ОДНОЙ строкой байт для обоих вызовов,
// различается только schemaVer — значит разница во времени между двумя
// бенчмарками может объясняться ТОЛЬКО кодом ветки switch (map-операции), а
// не разным объёмом разбираемого JSON.
var benchRaw = []byte(`{"type":"OxygenAdded","liters":42}`)

// BenchmarkUpcastV1Chain — schemaVer=1: полная цепочка из двух шагов
// (подставить unit, переименовать liters->amount) на каждый вызов.
func BenchmarkUpcastV1Chain(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := Upcast(benchRaw, 1); err != nil {
			b.Fatalf("upcast: %v", err)
		}
	}
}

// BenchmarkUpcastV3NoOp — schemaVer=3 на ТЕХ ЖЕ байтах: цикл цепочки не
// делает ни одной итерации, значит и ни одной map-операции. Результат при этом
// содержательно "неправильный" (Amount останется 0, потому что в benchRaw
// нет ключа "amount") — это ОЖИДАЕМО и не важно для бенчмарка: цель не
// корректность результата, а чистое время выполнения ветки без
// транформаций при одинаковом входе.
func BenchmarkUpcastV3NoOp(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := Upcast(benchRaw, 3); err != nil {
			b.Fatalf("upcast: %v", err)
		}
	}
}
