package service

import (
	"context"
	"testing"
)

// ПРИЁМ 3: дублёры. Ниже — три вида на одной задаче.
//
// Фейк — рабочая реализация порта, только упрощённая (список в памяти
// вместо Postgres).
// Мок — записывает вызовы, чтобы тест мог утверждать «кого позвали».
// Стаб — отдаёт заготовленный ответ, ничего не проверяет.
//
// Разница видна не на определениях, а на том, ЧТО утверждает тест. Проверить:
//
//	go test ./service/            # зелено: дефекта нет
//	go test -tags=bug ./service/  # падают фейк и мок с ЗАХВАТОМ аргумента,
//	                              # зелёной остаётся только слабая проверка
//	                              # числа вызовов — пропускает не мок, а слабое
//	                              # утверждение

// --- ФЕЙК: рабочая реализация в памяти -------------------------------------

type FakeStore struct {
	orders []Order
}

func (f *FakeStore) Save(_ context.Context, o Order) error {
	f.orders = append(f.orders, o)
	return nil
}

func (f *FakeStore) ByUser(_ context.Context, userID string) ([]Order, error) {
	var out []Order
	for _, o := range f.orders {
		if o.UserID == userID {
			out = append(out, o)
		}
	}
	return out, nil
}

// --- ШПИОН/МОК: записывает вызовы -------------------------------------------
//
// Строго по Месарошу это ШПИОН: он копит вызовы, а утверждает потом тест.
// Так же устроены mock()+verify() в Mockito и MagicMock в Python, поэтому
// дальше зовём его моком — как слово прижилось.

type MockStore struct {
	SaveCalls int
	Saved     []Order // захваченные аргументы: без них мок слеп к содержимому
}

func (m *MockStore) Save(_ context.Context, o Order) error {
	m.SaveCalls++
	m.Saved = append(m.Saved, o)
	return nil
}
func (m *MockStore) ByUser(context.Context, string) ([]Order, error) {
	return nil, nil
}

// --- СТАБ: заготовленный ответ, без проверок -------------------------------

type StubNotifier struct{ err error }

func (s StubNotifier) Notify(context.Context, string, string) error { return s.err }

// Тест на фейке говорит о РЕЗУЛЬТАТЕ: заказ сохранён и читается обратно с
// правильной ценой. Под тегом bug этот тест падает — и правильно делает.
func TestНаФейке_ЗаказСохранёнСоСкидкой(t *testing.T) {
	store := &FakeStore{}
	svc := OrderService{Store: store, Notifier: StubNotifier{}}

	if _, err := svc.Create(context.Background(), "o-1", "u-1", 10_000); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.ByUser(context.Background(), "u-1")
	if err != nil {
		t.Fatalf("ByUser: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ожидали 1 заказ, получили %d", len(got))
	}
	if got[0].PriceCent != 9_500 {
		t.Errorf("к оплате %d коп., ожидали 9500 (100.00 минус 5%%)", got[0].PriceCent)
	}
}

// СЛАБАЯ проверка вызовов: хранилище позвали ровно один раз. Утверждение
// осмысленное (не потеряли и не задублировали запись), но про цену оно не знает
// ничего. Под тегом bug тест остаётся ЗЕЛЁНЫМ, хотя клиент платит полную сумму.
//
// Важно, чего этот тест НЕ доказывает: что моки «слепы к содержимому». Он
// доказывает ровно то, что слабое утверждение пропускает дефект — см. тест ниже.
func TestНаМоке_СлабаяПроверка_ТолькоЧислоВызовов(t *testing.T) {
	store := &MockStore{}
	svc := OrderService{Store: store, Notifier: StubNotifier{}}

	if _, err := svc.Create(context.Background(), "o-1", "u-1", 10_000); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if store.SaveCalls != 1 {
		t.Errorf("Save позвали %d раз, ожидали 1", store.SaveCalls)
	}
}

// СИЛЬНАЯ проверка вызовов: захватываем аргумент и смотрим, ЧТО именно понесли
// в хранилище. Под тегом bug падает — ровно как тест на фейке.
//
// Отсюда честный вывод всей тройки: дело не в «фейк умеет, мок не умеет», а в
// том, ЧТО утверждает тест. Мок с захватом аргумента ловит тот же дефект.
// Разница между фейком и моком не в силе, а в предмете: фейк утверждает про
// РЕЗУЛЬТАТ (заказ читается обратно со скидкой), мок — про ВЫЗОВ (мы понесли в
// порт правильные данные). Первое ближе к тому, ради чего писался код; второе
// незаменимо там, где вызов и есть обязательство.
func TestНаМоке_СильнаяПроверка_ЗахватАргумента(t *testing.T) {
	store := &MockStore{}
	svc := OrderService{Store: store, Notifier: StubNotifier{}}

	if _, err := svc.Create(context.Background(), "o-1", "u-1", 10_000); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if len(store.Saved) != 1 {
		t.Fatalf("ожидали один захваченный вызов, получили %d", len(store.Saved))
	}
	if store.Saved[0].PriceCent != 9_500 {
		t.Errorf("в хранилище понесли цену %d коп., ожидали 9500 (100.00 минус 5%%)",
			store.Saved[0].PriceCent)
	}
}

// Стаб на месте: ошибка уведомления не роняет заказ (уведомление не критично).
func TestНаСтабе_ОшибкаУведомленияНеРоняетЗаказ(t *testing.T) {
	store := &FakeStore{}
	svc := OrderService{Store: store, Notifier: StubNotifier{err: context.DeadlineExceeded}}

	if _, err := svc.Create(context.Background(), "o-1", "u-1", 5_000); err != nil {
		t.Fatalf("уведомление упало, но заказ должен быть принят: %v", err)
	}
	if got, _ := store.ByUser(context.Background(), "u-1"); len(got) != 1 {
		t.Fatalf("заказ не сохранён")
	}
}
