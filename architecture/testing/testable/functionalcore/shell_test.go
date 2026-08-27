package functionalcore

import (
	"context"
	"errors"
	"testing"
	"time"
)

// --- фейки границ ---

type fakeRepo struct {
	order   Order
	loadErr error
	saveErr error
	saved   []int64
}

func (r *fakeRepo) Load(context.Context, string) (Order, error) { return r.order, r.loadErr }
func (r *fakeRepo) SaveDiscount(_ context.Context, _ string, cents int64) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.saved = append(r.saved, cents)
	return nil
}

type fakeNotifier struct {
	texts     []string
	notifyErr error
}

func (n *fakeNotifier) Notify(_ context.Context, _, text string) error {
	if n.notifyErr != nil {
		return n.notifyErr
	}
	n.texts = append(n.texts, text)
	return nil
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// Оболочке нужны ТОЛЬКО тесты на проводку: что загрузили, что позвали ядро,
// что применили побочные эффекты. Ветки правил здесь не повторяем — они уже
// исчерпывающе закрыты в тесте ядра.
func TestService_Process_Проводка(t *testing.T) {
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepo{order: Order{UserID: "u1", TotalCents: 20_000, PlacedAt: base}}
	notifier := &fakeNotifier{}
	svc := Service{Repo: repo, Notifier: notifier, Clock: fixedClock{base}}

	d, err := svc.Process(context.Background(), "order-1")
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if d.DiscountCents != 2_000 {
		t.Errorf("решение ядра не доехало: %+v", d)
	}
	if len(repo.saved) != 1 || repo.saved[0] != 2_000 {
		t.Errorf("скидка не сохранена: %v", repo.saved)
	}
	if len(notifier.texts) != 1 {
		t.Fatalf("уведомлений = %d, want 1", len(notifier.texts))
	}
}

// Без скидки — не сохраняем и не уведомляем.
func TestService_Process_БезСкидкиНетПобочек(t *testing.T) {
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepo{order: Order{UserID: "u1", TotalCents: 500, PlacedAt: base}}
	notifier := &fakeNotifier{}
	svc := Service{Repo: repo, Notifier: notifier, Clock: fixedClock{base}}

	if _, err := svc.Process(context.Background(), "order-1"); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(repo.saved) != 0 || len(notifier.texts) != 0 {
		t.Errorf("лишние побочки: saved=%v notify=%v", repo.saved, notifier.texts)
	}
}

// Ошибки границ — три РАЗНЫЕ ветки с разными последствиями, и каждая своя:
// на Load мы вообще ничего не сделали, на SaveDiscount скидка не сохранена
// (уведомлять нельзя), на Notify скидка уже сохранена (побочка применена).
// Проверяем все три, а не только первую.
func TestService_Process_ОшибкиГраниц(t *testing.T) {
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	order := Order{UserID: "u1", TotalCents: 20_000, PlacedAt: base}

	t.Run("ошибка загрузки", func(t *testing.T) {
		loadErr := errors.New("база недоступна")
		notifier := &fakeNotifier{}
		repo := &fakeRepo{loadErr: loadErr}
		svc := Service{Repo: repo, Notifier: notifier, Clock: fixedClock{base}}

		_, err := svc.Process(context.Background(), "order-1")
		if !errors.Is(err, loadErr) {
			t.Fatalf("ждали обёрнутую ошибку загрузки, получили %v", err)
		}
		if len(repo.saved) != 0 || len(notifier.texts) != 0 {
			t.Error("после ошибки загрузки не должно быть побочек")
		}
	})

	t.Run("ошибка сохранения скидки", func(t *testing.T) {
		saveErr := errors.New("запись отклонена")
		notifier := &fakeNotifier{}
		repo := &fakeRepo{order: order, saveErr: saveErr}
		svc := Service{Repo: repo, Notifier: notifier, Clock: fixedClock{base}}

		_, err := svc.Process(context.Background(), "order-1")
		if !errors.Is(err, saveErr) {
			t.Fatalf("ждали обёрнутую ошибку сохранения, получили %v", err)
		}
		// уведомлять о скидке, которая не сохранилась, нельзя
		if len(notifier.texts) != 0 {
			t.Errorf("уведомили о несохранённой скидке: %v", notifier.texts)
		}
	})

	t.Run("ошибка уведомления", func(t *testing.T) {
		notifyErr := errors.New("канал недоступен")
		notifier := &fakeNotifier{notifyErr: notifyErr}
		repo := &fakeRepo{order: order}
		svc := Service{Repo: repo, Notifier: notifier, Clock: fixedClock{base}}

		_, err := svc.Process(context.Background(), "order-1")
		if !errors.Is(err, notifyErr) {
			t.Fatalf("ждали обёрнутую ошибку уведомления, получили %v", err)
		}
		// а вот скидка к этому моменту УЖЕ сохранена — частично применённый
		// эффект. Тест это фиксирует: поведение осознанное, а не случайное.
		if len(repo.saved) != 1 {
			t.Errorf("скидка должна была сохраниться до уведомления: %v", repo.saved)
		}
	})
}
