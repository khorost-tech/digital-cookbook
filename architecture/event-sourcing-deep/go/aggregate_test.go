package main

import (
	"math"
	"testing"
)

// Агрегат — чистая функция: given прошлые события, when команда,
// then новые события или ошибка. Без базы и без моков.
func TestTankRejectsOverConsumption(t *testing.T) {
	tank := Tank{}
	for _, e := range []Event{
		{Type: "TankRegistered", Liters: 0},
		{Type: "OxygenAdded", Liters: 100},
	} {
		tank = tank.Apply(e)
	}

	_, err := tank.Decide(Command{Type: "ConsumeOxygen", Liters: 150})
	if err == nil {
		t.Fatal("расход больше остатка обязан быть отвергнут, но ошибки нет")
	}
	if tank.Liters != 100 {
		t.Fatalf("состояние изменилось при отказе: %v", tank.Liters)
	}
}

func TestTankAcceptsValidConsumption(t *testing.T) {
	tank := Tank{}.Apply(Event{Type: "TankRegistered"}).Apply(Event{Type: "OxygenAdded", Liters: 100})

	evs, err := tank.Decide(Command{Type: "ConsumeOxygen", Liters: 30})
	if err != nil {
		t.Fatalf("допустимый расход отвергнут: %v", err)
	}
	if len(evs) != 1 || evs[0].Type != "OxygenConsumed" || evs[0].Liters != 30 {
		t.Fatalf("ожидалось одно OxygenConsumed на 30, получено %+v", evs)
	}
}

// TestTankRejectsInvalidCommands — табличная проверка входных условий Decide.
// Все эти случаи агрегат раньше пропускал: ревью указало, что ConsumeOxygen
// не проверял ни знак величины, ни факт регистрации резервуара.
//
// Самый дорогой из них — отрицательный расход: Apply вычитает пришедшее
// значение, поэтому ConsumeOxygen на -5 литров ПОПОЛНЯЛ резервуар на 5 и
// проходил проверку остатка (-5 > остатка ложно всегда). Команда «потратить»
// увеличивала запас, и в истории это оставалось навсегда.
//
// Таблица проверена на прежней версии Decide: пять строк из одиннадцати
// прошли как валидные команды — отрицательный и нулевой расход, NaN в
// расходе и в пополнении, пополнение до регистрации.
//
// Остальные шесть прежний Decide отвергал, но две из них — по случайной
// причине, а не по своей проверке: "+Inf" в расходе отсекался сравнением с
// остатком (Inf > 100 истинно), а расход до регистрации — тем же сравнением
// (1 > 0 истинно на пустом резервуаре). Обе прошли бы, будь остаток больше
// или величина меньше; строки оставлены в таблице именно поэтому.
func TestTankRejectsInvalidCommands(t *testing.T) {
	registered := Tank{}.Apply(Event{Type: "TankRegistered"}).Apply(Event{Type: "OxygenAdded", Liters: 100})

	cases := []struct {
		name  string
		tank  Tank
		cmd   Command
		about string
	}{
		{"расход отрицательный", registered, Command{Type: "ConsumeOxygen", Liters: -5},
			"вычитание отрицательного превращает расход в пополнение"},
		{"расход нулевой", registered, Command{Type: "ConsumeOxygen", Liters: 0},
			"событие, ничего не меняющее, остаётся в истории навсегда"},
		{"расход NaN", registered, Command{Type: "ConsumeOxygen", Liters: math.NaN()},
			"NaN проходит любое сравнение и необратимо отравляет свёртку"},
		{"расход +Inf", registered, Command{Type: "ConsumeOxygen", Liters: math.Inf(1)},
			"бесконечность в остатке ломает все последующие сравнения"},
		{"пополнение отрицательное", registered, Command{Type: "AddOxygen", Liters: -5},
			"пополнение на отрицательное — это расход в обход проверки остатка"},
		{"пополнение NaN", registered, Command{Type: "AddOxygen", Liters: math.NaN()},
			"то же отравление свёртки со стороны пополнения"},
		{"расход до регистрации", Tank{}, Command{Type: "ConsumeOxygen", Liters: 1},
			"стрим без TankRegistered в начале неотличим от стрима с потерянным первым событием"},
		{"пополнение до регистрации", Tank{}, Command{Type: "AddOxygen", Liters: 10},
			"то же самое со стороны пополнения"},
		{"повторная регистрация", registered, Command{Type: "RegisterTank"},
			"два TankRegistered в одном стриме"},
		{"расход больше остатка", registered, Command{Type: "ConsumeOxygen", Liters: 150},
			"основной инвариант домена"},
		{"неизвестная команда", registered, Command{Type: "DrainTank", Liters: 1},
			"опечатка в типе команды не должна проходить молча"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			evs, err := c.tank.Decide(c.cmd)
			if err == nil {
				t.Fatalf("команда принята, а должна быть отвергнута (%s); порождено %+v", c.about, evs)
			}
			if evs != nil {
				t.Fatalf("при отказе не должно быть событий, получено %+v", evs)
			}
		})
	}
}

// TestTankAcceptsRegistrationThenRefill — зеркало таблицы выше: проверки
// входных условий не должны отвергать законный путь.
func TestTankAcceptsRegistrationThenRefill(t *testing.T) {
	evs, err := Tank{}.Decide(Command{Type: "RegisterTank"})
	if err != nil {
		t.Fatalf("регистрация нового резервуара отвергнута: %v", err)
	}
	tank := Tank{}.Apply(evs[0])

	evs, err = tank.Decide(Command{Type: "AddOxygen", Liters: 42})
	if err != nil {
		t.Fatalf("пополнение зарегистрированного резервуара отвергнуто: %v", err)
	}
	if len(evs) != 1 || evs[0].Type != "OxygenAdded" || evs[0].Liters != 42 {
		t.Fatalf("ожидалось одно OxygenAdded на 42, получено %+v", evs)
	}
	if got := tank.Apply(evs[0]).Liters; got != 42 {
		t.Fatalf("остаток после пополнения: ожидалось 42, получено %v", got)
	}
}
