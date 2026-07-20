package main

import "testing"

func TestCountMinNeverUnderestimates(t *testing.T) {
	cm := NewCountMin(2000, 5)
	truth := map[string]uint64{}
	// поток: "hot" очень частый, плюс шум
	for i := 0; i < 100_000; i++ {
		key := "noise-" + string(rune('a'+i%26))
		cm.Add(key, 1)
		truth[key]++
	}
	cm.Add("hot", 50_000)
	truth["hot"] += 50_000

	for k, tv := range truth {
		if est := cm.Estimate(k); est < tv {
			t.Fatalf("занижение для %q: est=%d < truth=%d", k, est, tv)
		}
	}
}
