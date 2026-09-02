// Package registry — тонкий HTTP-клиент реестра схем Apicurio (API v3).
//
// Это ПЕРВАЯ внешняя зависимость стенда (Задача 7): до сих пор проба
// либо не выходила за пределы своего процесса вовсе, либо (Java)
// собиралась в чужом контейнере, но тоже не обращалась по сети во время
// самого измерения. Реестр — другое дело: он поднимается и гасится
// сценарием, у него есть состояние между вызовами (зарегистрированная
// схема, правило совместимости), и клиент обязан либо получить ответ по
// сети, либо честно вернуть ошибку — никакого локального запасного пути
// у него нет и не может быть.
//
// Конфигурация реестра — РЕШЕНИЕ КОНТРОЛЛЕРА, проверенное живьём (см.
// task-7-brief.md): хранилище «в памяти» у Apicurio не существует
// (падает с «No Registry storage variant defined for value mem»),
// рабочее сочетание — APICURIO_STORAGE_KIND=sql + APICURIO_STORAGE_SQL_KIND=h2.
//
// Формы запросов и ответов ниже сняты вручную (curl) с живого контейнера
// apicurio/apicurio-registry:latest перед тем, как писать этот файл, — а
// не восстановлены по памяти или документации. Конкретно:
//
//	POST /apis/registry/v3/groups/{group}/artifacts
//	  {"artifactId":"...","artifactType":"AVRO",
//	   "firstVersion":{"content":{"content":"<схема текстом>","contentType":"application/json"}}}
//	  -> 200, {"artifact":{...},"version":{"globalId":N,...}}
//
//	POST /apis/registry/v3/groups/{group}/artifacts/{id}/rules
//	  {"ruleType":"COMPATIBILITY","config":"BACKWARD"} -> 204
//
//	POST /apis/registry/v3/groups/{group}/artifacts/{id}/versions
//	  {"content":{"content":"<схема текстом>","contentType":"application/json"}}
//	  -> 200 (совместима, {"globalId":N,...}) | 400 (несовместима,
//	  {"status":400,"name":"RuleViolationException","detail":"..."}) |
//	  422 ({"name":"UnprocessableSchemaException",...} — реестр не смог
//	  ВООБЩЕ оценить совместимость; см. spec.md, находка про alias_conflict)
//
//	GET /apis/registry/v3/ids/globalIds/{id} -> 200, тело — содержимое схемы как есть
package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client — клиент одного экземпляра реестра.
//
// Calls считает КАЖДОЕ фактическое обращение по сети — это и есть
// величина требования 4 брифа («сколько обращений к реестру нужно до
// первого чтения при холодном кэше»): счётчик не эвристика и не
// документированное число API, а то, что клиент реально сделал.
type Client struct {
	baseURL string
	http    *http.Client

	mu    sync.Mutex
	calls int
}

// New создаёт клиент. Таймаут короткий и намеренный: проба должна
// быстро отличить «реестр ответил» от «реестра нет», а не зависать на
// системном таймауте TCP (минуты) при погашенном контейнере.
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

// Calls — число фактических HTTP-обращений с момента создания клиента.
func (c *Client) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.http.Do(req)
}

type createArtifactResponse struct {
	Version struct {
		GlobalID int64 `json:"globalId"`
	} `json:"version"`
}

// CreateArtifact регистрирует артефакт с ПЕРВОЙ версией сразу — базовой
// схемой. Один вызов — одно обращение по сети (Calls растёт на 1).
func (c *Client) CreateArtifact(group, artifactID, artifactType, content, contentType string) (globalID int64, httpStatus int, err error) {
	body := map[string]any{
		"artifactId":   artifactID,
		"artifactType": artifactType,
		"firstVersion": map[string]any{
			"content": map[string]any{
				"content":     content,
				"contentType": contentType,
			},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, 0, fmt.Errorf("registry: сборка тела CreateArtifact: %w", err)
	}
	url := fmt.Sprintf("%s/apis/registry/v3/groups/%s/artifacts", c.baseURL, group)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("registry: CreateArtifact %s: %w", artifactID, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, resp.StatusCode, fmt.Errorf("registry: CreateArtifact %s: неожиданный статус %d: %s", artifactID, resp.StatusCode, respBody)
	}
	var parsed createArtifactResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return 0, resp.StatusCode, fmt.Errorf("registry: CreateArtifact %s: тело ответа не разобрать: %w (тело: %s)", artifactID, err, respBody)
	}
	return parsed.Version.GlobalID, resp.StatusCode, nil
}

// SetCompatibilityRule включает правило совместимости на артефакте.
// Apicurio отвечает 204 без тела.
func (c *Client) SetCompatibilityRule(group, artifactID, config string) (httpStatus int, err error) {
	body, _ := json.Marshal(map[string]string{"ruleType": "COMPATIBILITY", "config": config})
	url := fmt.Sprintf("%s/apis/registry/v3/groups/%s/artifacts/%s/rules", c.baseURL, group, artifactID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return 0, fmt.Errorf("registry: SetCompatibilityRule %s/%s: %w", artifactID, config, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNoContent {
		return resp.StatusCode, fmt.Errorf("registry: SetCompatibilityRule %s/%s: неожиданный статус %d: %s", artifactID, config, resp.StatusCode, respBody)
	}
	return resp.StatusCode, nil
}

// AddVersion — ГЛАВНЫЙ вызов требования 1 брифа: «точка отказа у
// реестра — ДО записи». Реестр отвечает 2xx (совместима, новая версия
// принята) либо любым другим статусом (несовместима ИЛИ сам не смог
// оценить схему — 422 у Apicurio означает именно это, см. doc.go). Тело
// ответа возвращается ВСЕГДА — это диагностика для отчёта, не часть
// контракта сравнения (тот же принцип, что и поле error строки
// результата остальных проб стенда, spec.md §11).
//
// err не равен nil только при ОШИБКЕ ТРАНСПОРТА (реестр недоступен) —
// отказ реестра принять схему это НЕ ошибка Go-уровня, а обычный
// httpStatus, который вызывающая сторона обязана разобрать сама.
func (c *Client) AddVersion(group, artifactID, content, contentType string) (globalID int64, httpStatus int, body string, err error) {
	reqBody, _ := json.Marshal(map[string]any{
		"content": map[string]any{"content": content, "contentType": contentType},
	})
	url := fmt.Sprintf("%s/apis/registry/v3/groups/%s/artifacts/%s/versions", c.baseURL, group, artifactID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return 0, 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return 0, 0, "", fmt.Errorf("registry: AddVersion %s: %w", artifactID, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var parsed struct {
			GlobalID int64 `json:"globalId"`
		}
		_ = json.Unmarshal(respBody, &parsed)
		return parsed.GlobalID, resp.StatusCode, string(respBody), nil
	}
	return 0, resp.StatusCode, string(respBody), nil
}

// FetchByGlobalID получает содержимое схемы по глобальному идентификатору
// — ровно то обращение, которое обязан сделать честный читатель Avro без
// собственного кэша схем, прежде чем декодировать первый байт данных.
//
// Погашенный реестр возвращает ОШИБКУ ТРАНСПОРТА (err != nil), не ответ:
// это и есть путь, которым Задача 7 доказывает недоступностью, что без
// схемы писателя Avro прочитать нельзя (см. needprobe, шаг registry_down).
func (c *Client) FetchByGlobalID(globalID int64) (content string, httpStatus int, err error) {
	url := fmt.Sprintf("%s/apis/registry/v3/ids/globalIds/%d", c.baseURL, globalID)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := c.do(req)
	if err != nil {
		return "", 0, fmt.Errorf("registry: FetchByGlobalID %d: %w", globalID, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, fmt.Errorf("registry: FetchByGlobalID %d: неожиданный статус %d: %s", globalID, resp.StatusCode, respBody)
	}
	return string(respBody), resp.StatusCode, nil
}
