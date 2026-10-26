package main

import (
	"encoding/json"
	"fmt"
)

// EventV3 — текущая форма события. Код домена знает ТОЛЬКО её.
// Это и есть смысл upcasting: старые формы живут в сторе вечно, но наружу
// из слоя чтения выходит одна.
type EventV3 struct {
	Type   string  `json:"type"`
	Amount float64 `json:"amount"`
	Unit   string  `json:"unit"`
}

// currentSchemaVer — версия формы, которую знает код домена (EventV3).
// Всё, что в сторе лежит с меньшей версией, поднимается до неё цепочкой.
const currentSchemaVer = 3

// upcastStep — один шаг цепочки: поднимает событие с версии v на v+1.
// Шаг видит только сам map события и не знает ни своей позиции в цепочке,
// ни того, какие шаги идут до и после него.
type upcastStep func(map[string]any)

// upcastSteps — цепочка, индексированная ИСХОДНОЙ версией шага: ключ v
// означает «шаг v -> v+1».
//
// Расширение цепочки на v3->v4 — это ровно две правки, ни одна из которых
// не трогает существующие шаги: дописать сюда запись `3: func(...)` и
// поднять currentSchemaVer до 4. Раньше здесь был switch с fallthrough,
// где добавление v4 требовало ДОПИСАТЬ fallthrough в существующую ветку
// v2->v3 — то есть править уже работающий шаг; ревью справедливо указало,
// что заявленной расширяемости у той конструкции не было.
//
// Полнота цепочки — не обещание в комментарии, а проверка:
// TestUpcastChainIsComplete падает, если currentSchemaVer подняли, а шаг
// добавить забыли, и Upcast возвращает ошибку, если дыра всё же случилась
// в рантайме.
var upcastSteps = map[int]upcastStep{
	// v1 -> v2: единица измерения не хранилась, подразумевались литры.
	1: func(m map[string]any) {
		m["unit"] = "liters"
	},
	// v2 -> v3: liters переименовано в amount.
	2: func(m map[string]any) {
		if v, ok := m["liters"]; ok {
			m["amount"] = v
			delete(m, "liters")
		}
	},
}

// Upcast поднимает событие любой известной версии до currentSchemaVer,
// применяя шаги цепочки подряд: v1 проходит оба шага, v2 — только второй,
// v3 — ни одного.
//
// Диапазон версий проверяется явно с ОБЕИХ сторон. Верхняя граница ловит
// событие из будущего (код старее данных — обычная ситуация при откате
// релиза), нижняя — 0 и отрицательные значения: раньше их принимала ветка
// `schemaVer <= 1`, то есть отсутствующий или битый schema_ver молча
// трактовался как v1 и проходил всю цепочку, будто это осмысленная старая
// форма.
func Upcast(raw []byte, schemaVer int) (EventV3, error) {
	if schemaVer < 1 {
		return EventV3{}, fmt.Errorf("недопустимая версия схемы %d: версии нумеруются с 1", schemaVer)
	}
	if schemaVer > currentSchemaVer {
		return EventV3{}, fmt.Errorf("схема версии %d новее кода (код знает до %d)", schemaVer, currentSchemaVer)
	}

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return EventV3{}, err
	}

	for v := schemaVer; v < currentSchemaVer; v++ {
		step, ok := upcastSteps[v]
		if !ok {
			return EventV3{}, fmt.Errorf("в цепочке нет шага v%d->v%d", v, v+1)
		}
		step(m)
	}

	out, err := json.Marshal(m)
	if err != nil {
		return EventV3{}, err
	}
	var e EventV3
	return e, json.Unmarshal(out, &e)
}
