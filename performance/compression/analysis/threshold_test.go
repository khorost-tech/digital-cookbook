package analysis

import (
	"math"
	"testing"
)

// Формула режима «горячий транспорт»: сжатие окупается при V > B·r/(r−1),
// откуда пороговая полоса B* = V·(r−1)/r.
func TestThresholdBandwidth(t *testing.T) {
	// При ratio=2 порог равен половине скорости сжатия.
	got, err := ThresholdBandwidthMBs(200, 2)
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	if math.Abs(got-100) > 1e-9 {
		t.Fatalf("B*=%v, ожидалось 100", got)
	}
	// При росте ratio порог стремится к самой скорости сжатия.
	got5, _ := ThresholdBandwidthMBs(200, 5)
	if math.Abs(got5-160) > 1e-9 {
		t.Fatalf("B*=%v при ratio=5, ожидалось 160", got5)
	}
	if got5 <= got {
		t.Fatal("порог обязан расти с ростом ratio")
	}
}

func TestThresholdRejectsNoGain(t *testing.T) {
	// ratio <= 1 означает, что сжатие не уменьшает объём: порога не существует.
	if _, err := ThresholdBandwidthMBs(200, 1); err == nil {
		t.Fatal("ожидалась ошибка при ratio=1")
	}
	if _, err := ThresholdBandwidthMBs(200, 0.9); err == nil {
		t.Fatal("ожидалась ошибка при ratio<1")
	}
}

func TestThresholdRejectsNonPositiveBandwidth(t *testing.T) {
	// Скорость сжатия <= 0 в предметной области не встречается (замер не
	// может дать неположительную пропускную способность) — это защита от
	// мусора на входе, а не рабочий сценарий.
	if _, err := ThresholdBandwidthMBs(0, 2); err == nil {
		t.Fatal("ожидалась ошибка при compressMBs=0")
	}
	if _, err := ThresholdBandwidthMBs(-5, 2); err == nil {
		t.Fatal("ожидалась ошибка при compressMBs<0")
	}
}
