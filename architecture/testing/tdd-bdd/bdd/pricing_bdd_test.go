// Package bdd прогоняет ту же доменную логику (tdd.Total), что и стенд TDD,
// но через BDD-сценарии на Gherkin (godog). Цель — показать стиль «спека
// через примеры» и честно сопоставить его с обычным table-тестом
// (см. pricing_table_test.go).
package bdd

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"tech.khorost/tdd-bdd-cookbook/tdd"
)

// cart — состояние сценария; перед каждым сценарием обнуляется в хуке Before.
type cart struct {
	items []tdd.Item
}

// rublesToCents переводит «100.00» в 10000 копеек. Такой «клей» между
// человеческим текстом сценария и кодом — неизбежная часть BDD-инструмента.
func rublesToCents(s string) (int64, error) {
	s = strings.TrimSpace(s)
	whole, frac, _ := strings.Cut(s, ".")
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("некорректная цена %q: %w", s, err)
	}
	frac = (frac + "00")[:2]
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("некорректная копеечная часть %q: %w", s, err)
	}
	return w*100 + f, nil
}

func (c *cart) anEmptyCart() error {
	c.items = nil
	return nil
}

func (c *cart) iAddAnItem(price string, qty int64) error {
	cents, err := rublesToCents(price)
	if err != nil {
		return err
	}
	c.items = append(c.items, tdd.Item{PriceCents: cents, Qty: qty})
	return nil
}

func (c *cart) theCartTotalIs(expected string) error {
	want, err := rublesToCents(expected)
	if err != nil {
		return err
	}
	got := tdd.Total(c.items)
	if got != want {
		return fmt.Errorf("итог корзины = %d коп., ожидали %d коп.", got, want)
	}
	return nil
}

// InitializeScenario связывает шаги Gherkin с методами — по одному регэкспу
// на шаг. Это и есть «step definitions»: слой, который надо писать и держать
// в синхроне с текстом фичи.
func InitializeScenario(sc *godog.ScenarioContext) {
	c := &cart{}
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		c.items = nil
		return ctx, nil
	})
	sc.Step(`^пустая корзина$`, c.anEmptyCart)
	sc.Step(`^я добавляю товар за ([\d.]+) в количестве (\d+)$`, c.iAddAnItem)
	sc.Step(`^итог корзины равен ([\d.]+)$`, c.theCartTotalIs)
}

// TestFeatures запускает все .feature из каталога features как обычный go test.
func TestFeatures(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: InitializeScenario,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"features"},
			TestingT: t,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("есть несоответствия между сценариями и шагами")
	}
}
