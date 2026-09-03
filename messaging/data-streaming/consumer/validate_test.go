package main

import "testing"

func TestParseValidTotal(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
		want    total
	}{
		{
			name:    "пустой объект — customer_id=0, orders=0",
			raw:     `{}`,
			wantErr: true,
		},
		{
			name:    "только customer_id, orders/total отсутствуют (=0)",
			raw:     `{"customer_id":1}`,
			wantErr: true,
		},
		{
			name:    "customer_id вне диапазона 1..5",
			raw:     `{"customer_id":123,"orders":5,"total":10}`,
			wantErr: true,
		},
		{
			name:    "customer_id=0 явно",
			raw:     `{"customer_id":0,"orders":5,"total":10}`,
			wantErr: true,
		},
		{
			name:    "orders=0 явно, customer_id валиден",
			raw:     `{"customer_id":2,"orders":0,"total":0}`,
			wantErr: true,
		},
		{
			// P2(ремедиация 4): total ОТСУТСТВУЕТ, orders присутствует и ненулевой
			// (в отличие от кейса "только customer_id" выше, где падает уже
			// проверка orders==0) — это единственный кейс, который до этой правки
			// проходил бы как валидная запись с total=0, хотя total нигде явно не
			// присылался.
			name:    "orders присутствует, total ОТСУТСТВУЕТ",
			raw:     `{"customer_id":1,"orders":1}`,
			wantErr: true,
		},
		{
			// P2(ремедиация 4): total ЯВНО равен 0 (в отличие от предыдущего кейса,
			// где поле отсутствует вовсе) — валидная запись, присутствие поля важнее
			// его значения, orders/total>0 не проверяется.
			name:    "total явно равен 0 (присутствует, не отсутствует)",
			raw:     `{"customer_id":1,"orders":1,"total":0}`,
			wantErr: false,
			want:    total{CustomerID: 1, Orders: 1, Total: 0},
		},
		{
			name:    "битый JSON",
			raw:     `not-a-json{{{`,
			wantErr: true,
		},
		{
			name:    "валидная запись",
			raw:     `{"customer_id":3,"orders":5,"total":10.5}`,
			wantErr: false,
			want:    total{CustomerID: 3, Orders: 5, Total: 10.5},
		},
		{
			name:    "валидная запись, customer_id на границе диапазона (5)",
			raw:     `{"customer_id":5,"orders":1,"total":0}`,
			wantErr: false,
			want:    total{CustomerID: 5, Orders: 1, Total: 0},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseValidTotal([]byte(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseValidTotal(%q) = %+v, nil; хотели ошибку", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseValidTotal(%q) вернул неожиданную ошибку: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("parseValidTotal(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseValidOrderEvent(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
		want    orderEvent
	}{
		{
			name:    "order_id=0 (поле отсутствует)",
			raw:     `{"customer_id":2}`,
			wantErr: true,
		},
		{
			name:    "order_id=0 явно",
			raw:     `{"order_id":0,"customer_id":2}`,
			wantErr: true,
		},
		{
			name:    "customer_id вне диапазона 1..5",
			raw:     `{"order_id":7,"customer_id":999}`,
			wantErr: true,
		},
		{
			name:    "customer_id=0",
			raw:     `{"order_id":7,"customer_id":0}`,
			wantErr: true,
		},
		{
			name:    "битый JSON",
			raw:     `not-a-json{{{`,
			wantErr: true,
		},
		{
			name:    "валидное событие",
			raw:     `{"amount":59.68,"order_id":1,"customer_id":2}`,
			wantErr: false,
			want:    orderEvent{OrderID: 1, CustomerID: 2},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseValidOrderEvent([]byte(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseValidOrderEvent(%q) = %+v, nil; хотели ошибку", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseValidOrderEvent(%q) вернул неожиданную ошибку: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("parseValidOrderEvent(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestDupFactor покрывает логику подсчёта дублей: одно сообщение с -dup
// должно дать 2 строки на вставку, без -dup — 1 (см. dupFactor в
// validate.go и его использование в обоих режимах run() в main.go).
func TestDupFactor(t *testing.T) {
	if got := dupFactor(false); got != 1 {
		t.Fatalf("dupFactor(false) = %d, want 1", got)
	}
	if got := dupFactor(true); got != 2 {
		t.Fatalf("dupFactor(true) = %d, want 2", got)
	}
}
