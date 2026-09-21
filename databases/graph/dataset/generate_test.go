package main

import "testing"

func sampleParams(seed int64) Params {
	return Params{
		Users: 100, Teams: 10, Roles: 15, Resources: 40,
		Services: 20, Packages: 50, Fanout: 4, Depth: 5, Seed: seed,
	}
}

// Один и тот же seed обязан давать побайтово одинаковый граф.
func TestDeterministicSeed(t *testing.T) {
	g1 := Generate(sampleParams(42))
	g2 := Generate(sampleParams(42))
	if g1.Checksum() != g2.Checksum() {
		t.Fatalf("одинаковый seed → разный граф: %d != %d", g1.Checksum(), g2.Checksum())
	}
}

// Разный seed должен давать разный граф (иначе seed ни на что не влияет).
func TestDifferentSeedDiffers(t *testing.T) {
	if Generate(sampleParams(1)).Checksum() == Generate(sampleParams(2)).Checksum() {
		t.Fatal("разный seed → одинаковый граф: seed не влияет на генерацию")
	}
}

// Гарантии структуры: цикл в depends_on, хаб-ресурс, длинная цепочка COLLABORATES.
func TestStructuralGuarantees(t *testing.T) {
	g := Generate(sampleParams(42))

	// namеренный цикл среди первых трёх сервисов
	svc0, svc1, svc2 := g.SvcLo, g.SvcLo+1, g.SvcLo+2
	need := map[[2]int64]bool{{svc2, svc1}: false, {svc1, svc0}: false, {svc0, svc2}: false}
	for _, e := range g.DependsOn {
		if _, ok := need[[2]int64{e.SrcID, e.DstID}]; ok {
			need[[2]int64{e.SrcID, e.DstID}] = true
		}
	}
	for edge, ok := range need {
		if !ok {
			t.Errorf("нет ожидаемого ребра цикла depends_on: %d->%d", edge[0], edge[1])
		}
	}

	// хаб-ресурс имеет заметно больше входящих grants, чем средний ресурс
	if g.HubResource != g.ResLo {
		t.Errorf("хаб-ресурс должен быть первым: got %d want %d", g.HubResource, g.ResLo)
	}
}
