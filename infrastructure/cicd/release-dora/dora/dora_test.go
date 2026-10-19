package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func sampleDeploys() []Deploy {
	return []Deploy{
		{ID: "1", Commit: ts("2026-01-01T09:00:00Z"), Deploy: ts("2026-01-01T11:00:00Z")}, // lead 2h
		{ID: "2", Commit: ts("2026-01-02T09:00:00Z"), Deploy: ts("2026-01-02T13:00:00Z")}, // lead 4h
		{ID: "3", Commit: ts("2026-01-03T09:00:00Z"), Deploy: ts("2026-01-03T15:00:00Z")}, // lead 6h
		{ID: "4", Commit: ts("2026-01-04T09:00:00Z"), Deploy: ts("2026-01-04T12:00:00Z"),
			Rework: true}, // lead 3h, незапланированная доработка после инцидента
	}
}

func sampleIncidents() []Incident {
	return []Incident{
		{DeployID: "2", Start: ts("2026-01-02T14:00:00Z"), Resolve: ts("2026-01-02T15:00:00Z")}, // 1h
	}
}

func TestDeploymentFrequency(t *testing.T) {
	if got := DeploymentFrequency(sampleDeploys(), 4); got != 1.0 {
		t.Errorf("DeploymentFrequency = %v, want 1.0", got)
	}
	if got := DeploymentFrequency(sampleDeploys(), 0); got != 0 {
		t.Errorf("DeploymentFrequency при periodDays=0 = %v, want 0", got)
	}
}

func TestChangeLeadTime(t *testing.T) {
	// leads: 2h,4h,6h,3h -> sorted 2,3,4,6 -> медиана (3+4)/2 = 3.5h.
	got := ChangeLeadTime(sampleDeploys())
	want := 3*time.Hour + 30*time.Minute
	if got != want {
		t.Errorf("ChangeLeadTime = %v, want %v", got, want)
	}
}

func TestFailedDeploymentRecoveryTime(t *testing.T) {
	if got := FailedDeploymentRecoveryTime(sampleIncidents()); got != time.Hour {
		t.Errorf("FailedDeploymentRecoveryTime = %v, want 1h", got)
	}
	if got := FailedDeploymentRecoveryTime(nil); got != 0 {
		t.Errorf("FailedDeploymentRecoveryTime без инцидентов = %v, want 0", got)
	}
}

func TestChangeFailRate(t *testing.T) {
	// 1 деплой из 4 вызвал инцидент => 0.25.
	if got := ChangeFailRate(sampleDeploys(), sampleIncidents()); got != 0.25 {
		t.Errorf("ChangeFailRate = %v, want 0.25", got)
	}
}

func TestChangeFailRateDistinctDeploys(t *testing.T) {
	// Два инцидента одного деплоя считаются как один упавший деплой.
	inc := []Incident{
		{DeployID: "2", Start: ts("2026-01-02T14:00:00Z"), Resolve: ts("2026-01-02T15:00:00Z")},
		{DeployID: "2", Start: ts("2026-01-02T16:00:00Z"), Resolve: ts("2026-01-02T17:00:00Z")},
	}
	if got := ChangeFailRate(sampleDeploys(), inc); got != 0.25 {
		t.Errorf("ChangeFailRate (два инцидента одного деплоя) = %v, want 0.25", got)
	}
}

// Инцидент с неизвестным deploy_id не должен раздувать числитель: иначе
// change fail rate способен превысить 100%.
func TestChangeFailRateIgnoresUnknownDeployIDs(t *testing.T) {
	inc := []Incident{
		{DeployID: "2", Start: ts("2026-01-02T14:00:00Z"), Resolve: ts("2026-01-02T15:00:00Z")},
		{DeployID: "нет-такого", Start: ts("2026-01-02T14:00:00Z"), Resolve: ts("2026-01-02T15:00:00Z")},
	}
	got := ChangeFailRate(sampleDeploys(), inc)
	if got != 0.25 {
		t.Errorf("ChangeFailRate с неизвестным deploy_id = %v, want 0.25", got)
	}
	if got > 1.0 {
		t.Errorf("ChangeFailRate = %v — превысил 100%%", got)
	}
}

func TestValidateIncidents(t *testing.T) {
	if err := ValidateIncidents(sampleDeploys(), sampleIncidents()); err != nil {
		t.Errorf("ValidateIncidents на корректных данных вернул ошибку: %v", err)
	}
	bad := []Incident{{DeployID: "нет-такого", Start: ts("2026-01-02T14:00:00Z"), Resolve: ts("2026-01-02T15:00:00Z")}}
	if err := ValidateIncidents(sampleDeploys(), bad); err == nil {
		t.Error("ValidateIncidents принял инцидент с неизвестным deploy_id")
	}
}

func TestDeploymentReworkRate(t *testing.T) {
	// 1 из 4 деплоев помечен rework => 0.25.
	if got := DeploymentReworkRate(sampleDeploys()); got != 0.25 {
		t.Errorf("DeploymentReworkRate = %v, want 0.25", got)
	}
	if got := DeploymentReworkRate(nil); got != 0 {
		t.Errorf("DeploymentReworkRate без деплоев = %v, want 0", got)
	}
}

// Рваный CSV должен давать внятную ошибку, а не панику по индексу.
func TestReadDeploysMalformedCSV(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.csv")
	if err := os.WriteFile(p, []byte("id,commit_time,deploy_time\nтолько-одна-колонка\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readDeploys(p); err == nil {
		t.Error("readDeploys принял строку с недостающими колонками")
	}
}

func TestReadDeploysBadDate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad-date.csv")
	body := "id,commit_time,deploy_time\n1,вчера,2026-01-01T11:00:00Z\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readDeploys(p); err == nil {
		t.Error("readDeploys принял неразбираемую дату")
	}
}

func TestReadDeploysDuplicateID(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "dup.csv")
	body := "id,commit_time,deploy_time\n" +
		"1,2026-01-01T09:00:00Z,2026-01-01T11:00:00Z\n" +
		"1,2026-01-02T09:00:00Z,2026-01-02T11:00:00Z\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readDeploys(p); err == nil {
		t.Error("readDeploys принял повторяющийся id")
	}
}

// Невозможные данные должны отвергаться, а не давать отрицательные длительности
// и молча смещать медианы.
func TestReadDeploysRejectsImpossibleRows(t *testing.T) {
	cases := map[string]string{
		"deploy раньше commit": "id,commit_time,deploy_time\n1,2026-01-01T11:00:00Z,2026-01-01T09:00:00Z\n",
		"пустой id":            "id,commit_time,deploy_time\n ,2026-01-01T09:00:00Z,2026-01-01T11:00:00Z\n",
		"неизвестный kind":     "id,commit_time,deploy_time,kind\n1,2026-01-01T09:00:00Z,2026-01-01T11:00:00Z,rewrok\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "d.csv")
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := readDeploys(p); err == nil {
				t.Errorf("readDeploys принял невозможные данные (%s)", name)
			}
		})
	}
}

func TestReadIncidentsRejectsImpossibleRows(t *testing.T) {
	cases := map[string]string{
		"resolve раньше start": "deploy_id,start,resolve\n1,2026-01-02T15:00:00Z,2026-01-02T14:00:00Z\n",
		"пустой deploy_id":     "deploy_id,start,resolve\n ,2026-01-02T14:00:00Z,2026-01-02T15:00:00Z\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "i.csv")
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := readIncidents(p); err == nil {
				t.Errorf("readIncidents принял невозможные данные (%s)", name)
			}
		})
	}
}

// Инцидент не может начаться раньше деплоя, который его якобы вызвал.
func TestValidateIncidentsRejectsIncidentBeforeDeploy(t *testing.T) {
	early := []Incident{
		{DeployID: "2", Start: ts("2026-01-02T10:00:00Z"), Resolve: ts("2026-01-02T11:00:00Z")},
	}
	if err := ValidateIncidents(sampleDeploys(), early); err == nil {
		t.Error("ValidateIncidents принял инцидент, начавшийся раньше своего деплоя")
	}
}

func TestReadDeploysReworkFlag(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "rework.csv")
	body := "id,commit_time,deploy_time,kind\n" +
		"1,2026-01-01T09:00:00Z,2026-01-01T11:00:00Z,planned\n" +
		"2,2026-01-02T09:00:00Z,2026-01-02T11:00:00Z,rework\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ds, err := readDeploys(p)
	if err != nil {
		t.Fatal(err)
	}
	if ds[0].Rework || !ds[1].Rework {
		t.Errorf("флаг rework разобран неверно: %+v", ds)
	}
}
