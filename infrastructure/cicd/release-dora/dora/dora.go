// Калькулятор метрик DORA по CSV-фикстурам деплоев и инцидентов.
//
// Модель — актуальная, пятиметровая (DORA, 2024), а не «четыре ключа»:
//
//	Throughput (пропускная способность):
//	  1. deployment frequency            — как часто выкатываем;
//	  2. change lead time                — от коммита до прода;
//	  3. failed deployment recovery time — восстановление ПОСЛЕ НЕУДАЧНОГО ДЕПЛОЯ
//	     (переименование и переопределение прежней MTTR: та не отличала отказ
//	     из-за изменения от отказа по внешней причине).
//	Instability (нестабильность):
//	  4. change fail rate       — доля деплоев, приведших к отказу;
//	  5. deployment rework rate — доля деплоев, которые были незапланированной
//	     работой по устранению последствий инцидента в проде (добавлена в 2024).
//
// Числа — детерминированный расчёт по входным данным, не замер живой системы;
// ценность в методе расчёта и в том, как связка метрик ловит накрутку одной.
package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Deploy — один деплой. Kind различает плановую выкатку и незапланированную
// доработку по следам инцидента (нужна для deployment rework rate).
type Deploy struct {
	ID             string
	Commit, Deploy time.Time
	Rework         bool
}

// Incident — отказ в проде, вызванный конкретным деплоем. Recovery time
// считается только по таким отказам: метрика именно про неудачный деплой.
type Incident struct {
	DeployID       string
	Start, Resolve time.Time
}

// DeploymentFrequency — деплоев в день за период periodDays.
func DeploymentFrequency(deploys []Deploy, periodDays float64) float64 {
	if periodDays <= 0 {
		return 0
	}
	return float64(len(deploys)) / periodDays
}

// ChangeLeadTime — медиана времени от коммита до деплоя.
func ChangeLeadTime(deploys []Deploy) time.Duration {
	if len(deploys) == 0 {
		return 0
	}
	ds := make([]time.Duration, len(deploys))
	for i, d := range deploys {
		ds[i] = d.Deploy.Sub(d.Commit)
	}
	return medianDuration(ds)
}

// FailedDeploymentRecoveryTime — медиана времени восстановления по отказам,
// вызванным деплоями. Прежняя MTTR считала любое восстановление; здесь — только
// то, что инициировано изменением.
func FailedDeploymentRecoveryTime(incidents []Incident) time.Duration {
	if len(incidents) == 0 {
		return 0
	}
	ds := make([]time.Duration, len(incidents))
	for i, in := range incidents {
		ds[i] = in.Resolve.Sub(in.Start)
	}
	return medianDuration(ds)
}

// ChangeFailRate — доля деплоев, вызвавших хотя бы один инцидент (0..1).
// Несколько инцидентов одного деплоя считаются как один упавший деплой.
func ChangeFailRate(deploys []Deploy, incidents []Incident) float64 {
	if len(deploys) == 0 {
		return 0
	}
	return float64(len(FailedDeployIDs(deploys, incidents))) / float64(len(deploys))
}

// DeploymentReworkRate — доля деплоев, которые были незапланированной работой по
// устранению последствий инцидента (0..1).
func DeploymentReworkRate(deploys []Deploy) float64 {
	if len(deploys) == 0 {
		return 0
	}
	n := 0
	for _, d := range deploys {
		if d.Rework {
			n++
		}
	}
	return float64(n) / float64(len(deploys))
}

// FailedDeployIDs — множество деплоев, вызвавших инцидент. Учитываются только
// идентификаторы, реально присутствующие среди деплоев: иначе опечатка в
// incidents.csv раздувала бы числитель и давала change fail rate выше 100%.
func FailedDeployIDs(deploys []Deploy, incidents []Incident) map[string]bool {
	known := make(map[string]bool, len(deploys))
	for _, d := range deploys {
		known[d.ID] = true
	}
	failed := map[string]bool{}
	for _, in := range incidents {
		if known[in.DeployID] {
			failed[in.DeployID] = true
		}
	}
	return failed
}

// ValidateIncidents возвращает ошибку, если инцидент ссылается на несуществующий
// деплой либо начался раньше него. Тихо игнорировать такие строки нельзя: это
// молча искажает метрики, а инцидент до деплоя означает, что связь «этот деплой
// вызвал этот отказ» проставлена неверно.
func ValidateIncidents(deploys []Deploy, incidents []Incident) error {
	known := make(map[string]time.Time, len(deploys))
	for _, d := range deploys {
		known[d.ID] = d.Deploy
	}
	for _, in := range incidents {
		deployedAt, ok := known[in.DeployID]
		if !ok {
			return fmt.Errorf("инцидент ссылается на неизвестный deploy_id %q", in.DeployID)
		}
		if in.Start.Before(deployedAt) {
			return fmt.Errorf("инцидент по deploy_id %q начался (%s) раньше самого деплоя (%s)",
				in.DeployID, in.Start.Format(time.RFC3339), deployedAt.Format(time.RFC3339))
		}
	}
	return nil
}

func medianDuration(ds []time.Duration) time.Duration {
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	n := len(ds)
	if n%2 == 1 {
		return ds[n/2]
	}
	return (ds[n/2-1] + ds[n/2]) / 2
}

// readCSV читает CSV с заголовком, требуя ровно minFields колонок в каждой
// строке. csv.Reader сам отвергает рваные строки, но явная проверка даёт
// внятное сообщение вместо паники по индексу дальше по коду.
func readCSV(path string, minFields int) ([][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(rows) <= 1 {
		return nil, nil
	}
	for i, r := range rows[1:] {
		if len(r) < minFields {
			return nil, fmt.Errorf("%s: строка %d — %d колонок, нужно минимум %d",
				path, i+2, len(r), minFields)
		}
	}
	return rows[1:], nil
}

func parseTime(path string, line int, s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: строка %d — неверная дата %q", path, line, s)
	}
	return t, nil
}

// readDeploys читает id, commit_time, deploy_time[, kind]. kind="rework"
// помечает незапланированную выкатку по следам инцидента.
func readDeploys(path string) ([]Deploy, error) {
	rows, err := readCSV(path, 3)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]Deploy, 0, len(rows))
	for i, r := range rows {
		line := i + 2
		if strings.TrimSpace(r[0]) == "" {
			return nil, fmt.Errorf("%s: строка %d — пустой id", path, line)
		}
		if seen[r[0]] {
			return nil, fmt.Errorf("%s: строка %d — повторный id %q", path, line, r[0])
		}
		seen[r[0]] = true
		commit, err := parseTime(path, line, r[1])
		if err != nil {
			return nil, err
		}
		deploy, err := parseTime(path, line, r[2])
		if err != nil {
			return nil, err
		}
		// Деплой раньше собственного коммита даёт отрицательный lead time и молча
		// тянет медиану вниз — данные невозможные, а не просто странные.
		if deploy.Before(commit) {
			return nil, fmt.Errorf("%s: строка %d — deploy_time (%s) раньше commit_time (%s)",
				path, line, r[2], r[1])
		}
		d := Deploy{ID: r[0], Commit: commit, Deploy: deploy}
		// Неизвестный kind нельзя молча считать плановым: так опечатка «rewrok»
		// занизила бы deployment rework rate, не подав ни одного признака.
		if len(r) > 3 {
			switch kind := strings.TrimSpace(r[3]); kind {
			case "", "planned":
			case "rework":
				d.Rework = true
			default:
				return nil, fmt.Errorf("%s: строка %d — неизвестный kind %q (ожидается planned|rework)",
					path, line, kind)
			}
		}
		out = append(out, d)
	}
	return out, nil
}

func readIncidents(path string) ([]Incident, error) {
	rows, err := readCSV(path, 3)
	if err != nil {
		return nil, err
	}
	out := make([]Incident, 0, len(rows))
	for i, r := range rows {
		line := i + 2
		if strings.TrimSpace(r[0]) == "" {
			return nil, fmt.Errorf("%s: строка %d — пустой deploy_id", path, line)
		}
		start, err := parseTime(path, line, r[1])
		if err != nil {
			return nil, err
		}
		resolve, err := parseTime(path, line, r[2])
		if err != nil {
			return nil, err
		}
		// Восстановление раньше начала — отрицательная длительность, которая
		// молча занижает медиану recovery time.
		if resolve.Before(start) {
			return nil, fmt.Errorf("%s: строка %d — resolve (%s) раньше start (%s)",
				path, line, r[2], r[1])
		}
		out = append(out, Incident{DeployID: r[0], Start: start, Resolve: resolve})
	}
	return out, nil
}

func run(deploysPath, incidentsPath string, periodDays float64) error {
	deploys, err := readDeploys(deploysPath)
	if err != nil {
		return fmt.Errorf("чтение деплоев: %w", err)
	}
	incidents, err := readIncidents(incidentsPath)
	if err != nil {
		return fmt.Errorf("чтение инцидентов: %w", err)
	}
	if err := ValidateIncidents(deploys, incidents); err != nil {
		return err
	}

	freq := DeploymentFrequency(deploys, periodDays)
	lead := ChangeLeadTime(deploys)
	recov := FailedDeploymentRecoveryTime(incidents)
	cfr := ChangeFailRate(deploys, incidents)
	rework := DeploymentReworkRate(deploys)
	failed := len(FailedDeployIDs(deploys, incidents))

	fmt.Printf("деплоев: %d за %.0f дн.\n", len(deploys), periodDays)
	fmt.Println("-- throughput --")
	fmt.Printf("1. Deployment frequency:            %.3f деплоя/день  (= %d / %.0f)\n",
		freq, len(deploys), periodDays)
	fmt.Printf("2. Change lead time (медиана):       %s  (медиана deploy - commit)\n", lead)
	fmt.Printf("3. Failed deployment recovery (мед): %s  (медиана resolve - start по отказам от деплоя)\n", recov)
	fmt.Println("-- instability --")
	fmt.Printf("4. Change fail rate:                %.1f%%  (%d упавших деплоев из %d)\n",
		cfr*100, failed, len(deploys))
	fmt.Printf("5. Deployment rework rate:          %.1f%%  (%d незапланированных из %d)\n",
		rework*100, countRework(deploys), len(deploys))
	return nil
}

func countRework(deploys []Deploy) int {
	n := 0
	for _, d := range deploys {
		if d.Rework {
			n++
		}
	}
	return n
}

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintf(os.Stderr, "использование: dora <deployments.csv> <incidents.csv> <periodDays>\n")
		os.Exit(2)
	}
	periodDays, err := strconv.ParseFloat(os.Args[3], 64)
	if err != nil || periodDays <= 0 {
		fmt.Fprintf(os.Stderr, "periodDays должен быть положительным числом, получено %q\n", os.Args[3])
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2], periodDays); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}
