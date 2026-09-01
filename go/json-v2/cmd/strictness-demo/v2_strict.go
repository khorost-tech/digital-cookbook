//go:build goexperiment.jsonv2

// Третья колонка демо: те же случаи, поданные напрямую в encoding/json/v2, а
// не в encoding/json поверх него. Разница между v1-on-v2 и этой колонкой
// показывает, что строгость v2 живёт в API, а не в движке: encoding/json
// сохраняет прежнюю терпимость, даже работая поверх v2, а отказы даёт только
// явный переход на encoding/json/v2.
package main

import jsonv2 "encoding/json/v2"

// v2Available сообщает, собрана ли эта колонка в текущем режиме. В этом файле
// (тег goexperiment.jsonv2) она всегда true — иначе файл не попал бы в сборку.
const v2Available = true

// v2Verdict прогоняет один случай через encoding/json/v2 — тот же тип target
// и тот же вход, что и v1-колонка в main.go.
func v2Verdict(data string) string {
	var v target
	if err := jsonv2.Unmarshal([]byte(data), &v); err != nil {
		return "rejected"
	}
	return "accepted"
}
