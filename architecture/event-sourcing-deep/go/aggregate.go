package main

import (
	"fmt"
	"math"
)

// Event — факт в прошедшем времени. НЕ CRUD: OxygenConsumed, а не TankUpdated.
// Разница не косметическая: проекцию текущего значения на «Updated» построить
// можно, а вот величину и причину изменения — нет, не заглянув в предыдущее
// состояние.
type Event struct {
	Type   string  `json:"type"`
	Liters float64 `json:"liters"`
}

type Command struct {
	Type   string
	Liters float64
}

// Tank — агрегат «резервуар». Граница согласованности и оптимистичной
// блокировки: инвариант «остаток не уходит в минус» держится внутри него.
type Tank struct {
	Registered bool
	Liters     float64
}

// Apply — свёртка. Чистая функция: состояние + событие -> состояние.
//
// НЕТ ветки default: событие неизвестного типа проходит через switch, не
// меняя состояние, и молча игнорируется — Apply не возвращает ошибку и не
// паникует. Это ещё одна слепая зона golden-сверки (см. FIXTURES.md,
// «Артефакт 05», «не ловит»): порча event_type у строки в events (опечатка,
// неудачная миграция, обрезанные данные) не провалит ни replay-тест, ни
// сверку с expected.json — сумма просто не увеличится на то, что должно
// было прибавиться этим событием, и разойдётся с golden ТОЛЬКО если
// golden-снимок независимо пересчитан без этой порчи (как и сделано на
// шаге 3 «Артефакта 05») — но ничто в самом Apply не сигнализирует о факте
// пропуска.
func (t Tank) Apply(e Event) Tank {
	switch e.Type {
	case "TankRegistered":
		t.Registered = true
	case "OxygenAdded":
		t.Liters += e.Liters
	case "OxygenConsumed":
		t.Liters -= e.Liters
	}
	return t
}

// checkAmount — общая проверка величины для команд, несущих литры.
// Отрицательное значение здесь не «странный ввод», а смена смысла операции:
// Apply вычитает то, что пришло в OxygenConsumed, поэтому ConsumeOxygen с
// -5 литрами ПОПОЛНИЛ БЫ резервуар на 5, пройдя все остальные проверки
// (-5 > t.Liters ложно при любом неотрицательном остатке). Ноль порождает
// событие, ничего не меняющее, — мусор в неизменяемой истории навсегда.
// NaN и ±Inf отсекаются отдельно: любое сравнение с NaN ложно, поэтому
// NaN проскочил бы и первую, и вторую проверку и отравил бы остаток
// необратимо — свёртка после этого возвращает NaN на любом продолжении
// истории, и починить её нельзя ничем, кроме правки самих событий.
func checkAmount(liters float64) error {
	if math.IsNaN(liters) || math.IsInf(liters, 0) {
		return fmt.Errorf("величина должна быть конечным числом, получено %v", liters)
	}
	if liters <= 0 {
		return fmt.Errorf("величина должна быть строго положительной, получено %.3f", liters)
	}
	return nil
}

// Decide — решение. Тоже чистая функция: состояние + команда -> события
// или отказ. Ничего не пишет и ничего не знает про хранилище.
//
// Требование «резервуар зарегистрирован» — не формальность: незарегистрированный
// Tank имеет нулевой остаток, и AddOxygen на нём создал бы стрим, у которого
// нет TankRegistered в начале истории. Такой стрим потом невозможно отличить
// от стрима с потерянным первым событием.
func (t Tank) Decide(c Command) ([]Event, error) {
	switch c.Type {
	case "RegisterTank":
		if t.Registered {
			return nil, fmt.Errorf("резервуар уже зарегистрирован")
		}
		return []Event{{Type: "TankRegistered"}}, nil
	case "AddOxygen":
		if !t.Registered {
			return nil, fmt.Errorf("резервуар не зарегистрирован")
		}
		if err := checkAmount(c.Liters); err != nil {
			return nil, fmt.Errorf("пополнение: %w", err)
		}
		return []Event{{Type: "OxygenAdded", Liters: c.Liters}}, nil
	case "ConsumeOxygen":
		if !t.Registered {
			return nil, fmt.Errorf("резервуар не зарегистрирован")
		}
		if err := checkAmount(c.Liters); err != nil {
			return nil, fmt.Errorf("расход: %w", err)
		}
		if c.Liters > t.Liters {
			return nil, fmt.Errorf("расход %.3f больше остатка %.3f", c.Liters, t.Liters)
		}
		return []Event{{Type: "OxygenConsumed", Liters: c.Liters}}, nil
	}
	return nil, fmt.Errorf("неизвестная команда %q", c.Type)
}
