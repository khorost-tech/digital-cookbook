// Command greeter-operator — минимальный оператор Kubernetes для CRD Greeting.
//
// Показывает механику reconcile без controller-runtime и client-go: только
// стандартная библиотека. Доступ к Kubernetes API — обычный HTTPS-клиент с
// in-cluster токеном ServiceAccount и CA кластера (без InsecureSkipVerify),
// watch — построчное чтение потока ответа (newline-delimited JSON, как отдаёт
// сам apiserver).
//
// Цикл: на событие ADDED/MODIFIED greeting'а — reconcile(name); плюс
// периодический resync (полный проход по всем Greeting) на случай
// пропущенных событий watch. Reconcile идемпотентен: если ConfigMap уже в
// желаемом состоянии — оператор явно пишет это в лог и ничего не меняет.
//
// Единственная зависимость — стандартная библиотека Go:
// bufio, bytes, context, crypto/tls, crypto/x509, encoding/json, errors,
// fmt, io, log, net/http, os, os/signal, syscall, time.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	saDir          = "/var/run/secrets/kubernetes.io/serviceaccount"
	apiGroup       = "cookbook.khorost.tech"
	apiVersion     = "v1alpha1"
	resyncInterval = 30 * time.Second
	watchRetryWait = 5 * time.Second
	standLabel     = "stand"
	standValue     = "cookbook-operators"
)

// --- типы объектов Kubernetes API (только поля, нужные оператору) ---

type objectMeta struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace,omitempty"`
	Generation      int64             `json:"generation,omitempty"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
}

type greetingSpec struct {
	Who string `json:"who"`
}

type greetingStatus struct {
	Message            string `json:"message,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
}

type greeting struct {
	APIVersion string         `json:"apiVersion,omitempty"`
	Kind       string         `json:"kind,omitempty"`
	Metadata   objectMeta     `json:"metadata"`
	Spec       greetingSpec   `json:"spec"`
	Status     greetingStatus `json:"status"`
}

type greetingList struct {
	Items []greeting `json:"items"`
}

type watchEvent struct {
	Type   string          `json:"type"`
	Object json.RawMessage `json:"object"`
}

type configMap struct {
	APIVersion string            `json:"apiVersion,omitempty"`
	Kind       string            `json:"kind,omitempty"`
	Metadata   objectMeta        `json:"metadata"`
	Data       map[string]string `json:"data"`
}

// errNotFound — сигнальная ошибка для 404 от apiserver.
var errNotFound = errors.New("объект не найден")

// --- клиент Kubernetes API на чистом net/http ---

type k8sClient struct {
	http  *http.Client
	base  string
	token string
	ns    string
}

func newK8sClient() (*k8sClient, error) {
	tokenBytes, err := os.ReadFile(saDir + "/token")
	if err != nil {
		return nil, fmt.Errorf("чтение токена ServiceAccount: %w", err)
	}
	caBytes, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("чтение CA кластера: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBytes) {
		return nil, fmt.Errorf("CA кластера: не удалось разобрать PEM")
	}

	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, fmt.Errorf("не заданы KUBERNETES_SERVICE_HOST/KUBERNETES_SERVICE_PORT")
	}
	ns := os.Getenv("NAMESPACE")
	if ns == "" {
		return nil, fmt.Errorf("не задан NAMESPACE")
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}

	return &k8sClient{
		http:  &http.Client{Transport: transport},
		base:  fmt.Sprintf("https://%s:%s", host, port),
		token: string(bytes.TrimSpace(tokenBytes)),
		ns:    ns,
	}, nil
}

// do выполняет запрос к apiserver с авторизацией по токену ServiceAccount.
// Токен читается один раз при старте — в проде токены проецируются с TTL и
// перечитываются периодически, здесь — намеренное упрощение для демо.
func (c *k8sClient) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

// doJSON выполняет запрос и разбирает JSON-ответ в out (если not nil).
// Статус >=300 (кроме 404, который транслируется в errNotFound) — ошибка.
func (c *k8sClient) doJSON(ctx context.Context, method, path string, body []byte, out interface{}) error {
	resp, err := c.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("чтение ответа %s %s: %w", method, path, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: статус %d: %s", method, path, resp.StatusCode, string(respBody))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("разбор ответа %s %s: %w", method, path, err)
	}
	return nil
}

func (c *k8sClient) greetingsPath() string {
	return fmt.Sprintf("/apis/%s/%s/namespaces/%s/greetings", apiGroup, apiVersion, c.ns)
}

func (c *k8sClient) getGreeting(ctx context.Context, name string) (*greeting, error) {
	var g greeting
	if err := c.doJSON(ctx, http.MethodGet, c.greetingsPath()+"/"+name, nil, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

func (c *k8sClient) listGreetings(ctx context.Context) (*greetingList, error) {
	var l greetingList
	if err := c.doJSON(ctx, http.MethodGet, c.greetingsPath(), nil, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

// updateGreetingStatus пишет status через подресурс /status — так CRD с
// subresources.status и предполагается использовать: изменение status не
// трогает spec и не увеличивает metadata.generation.
func (c *k8sClient) updateGreetingStatus(ctx context.Context, g *greeting) error {
	body, err := json.Marshal(g)
	if err != nil {
		return err
	}
	return c.doJSON(ctx, http.MethodPut, c.greetingsPath()+"/"+g.Metadata.Name+"/status", body, nil)
}

func (c *k8sClient) configMapsPath() string {
	return fmt.Sprintf("/api/v1/namespaces/%s/configmaps", c.ns)
}

func (c *k8sClient) getConfigMap(ctx context.Context, name string) (*configMap, error) {
	var cm configMap
	if err := c.doJSON(ctx, http.MethodGet, c.configMapsPath()+"/"+name, nil, &cm); err != nil {
		return nil, err
	}
	return &cm, nil
}

func (c *k8sClient) createConfigMap(ctx context.Context, cm *configMap) error {
	body, err := json.Marshal(cm)
	if err != nil {
		return err
	}
	return c.doJSON(ctx, http.MethodPost, c.configMapsPath(), body, nil)
}

func (c *k8sClient) updateConfigMap(ctx context.Context, cm *configMap) error {
	body, err := json.Marshal(cm)
	if err != nil {
		return err
	}
	return c.doJSON(ctx, http.MethodPut, c.configMapsPath()+"/"+cm.Metadata.Name, body, nil)
}

// --- reconcile ---

// reconcile приводит ConfigMap greeting-<name> к желаемому состоянию,
// заданному Greeting.spec.who, и обновляет Greeting.status. Идемпотентен:
// если ConfigMap уже содержит нужное сообщение, изменений не производится —
// это явно видно в логе.
func reconcile(ctx context.Context, c *k8sClient, name string) {
	g, err := c.getGreeting(ctx, name)
	if errors.Is(err, errNotFound) {
		log.Printf("reconcile %s: Greeting не найден (удалён до обработки), пропускаю", name)
		return
	}
	if err != nil {
		log.Printf("reconcile %s: ошибка чтения Greeting: %v", name, err)
		return
	}

	desired := fmt.Sprintf("Привет, %s!", g.Spec.Who)
	cmName := "greeting-" + name

	cm, err := c.getConfigMap(ctx, cmName)
	switch {
	case errors.Is(err, errNotFound):
		log.Printf("reconcile %s: ConfigMap %s не найден — создаю с message=%q", name, cmName, desired)
		newCM := &configMap{
			APIVersion: "v1",
			Kind:       "ConfigMap",
			Metadata: objectMeta{
				Name:      cmName,
				Namespace: c.ns,
				Labels:    map[string]string{standLabel: standValue, "greeting": name},
			},
			Data: map[string]string{"message": desired},
		}
		if err := c.createConfigMap(ctx, newCM); err != nil {
			log.Printf("reconcile %s: создание ConfigMap %s не удалось: %v", name, cmName, err)
			return
		}
	case err != nil:
		log.Printf("reconcile %s: ошибка чтения ConfigMap %s: %v", name, cmName, err)
		return
	case cm.Data["message"] != desired:
		log.Printf("reconcile %s: ConfigMap %s расходится с желаемым (было %q, нужно %q) — обновляю",
			name, cmName, cm.Data["message"], desired)
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data["message"] = desired
		if cm.Metadata.Labels == nil {
			cm.Metadata.Labels = map[string]string{}
		}
		cm.Metadata.Labels[standLabel] = standValue
		cm.Metadata.Labels["greeting"] = name
		if err := c.updateConfigMap(ctx, cm); err != nil {
			log.Printf("reconcile %s: обновление ConfigMap %s не удалось: %v", name, cmName, err)
			return
		}
	default:
		log.Printf("reconcile %s: ConfigMap %s уже в желаемом состоянии (message=%q) — изменений не требуется", name, cmName, desired)
	}

	if g.Status.Message == desired && g.Status.ObservedGeneration == g.Metadata.Generation {
		log.Printf("reconcile %s: status уже актуален (message=%q, observedGeneration=%d) — не трогаю", name, desired, g.Metadata.Generation)
		return
	}

	g.Status.Message = desired
	g.Status.ObservedGeneration = g.Metadata.Generation
	if err := c.updateGreetingStatus(ctx, g); err != nil {
		log.Printf("reconcile %s: обновление status не удалось: %v", name, err)
		return
	}
	log.Printf("reconcile %s: status обновлён (message=%q, observedGeneration=%d)", name, desired, g.Metadata.Generation)
}

// --- resync: полный проход по всем Greeting, независимо от watch ---

func resyncLoop(ctx context.Context, c *k8sClient) {
	ticker := time.NewTicker(resyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			list, err := c.listGreetings(ctx)
			if err != nil {
				log.Printf("resync: список Greeting не получен: %v", err)
				continue
			}
			log.Printf("resync: полный проход, найдено Greeting: %d", len(list.Items))
			for _, item := range list.Items {
				reconcile(ctx, c, item.Metadata.Name)
			}
		}
	}
}

// --- watch: построчное чтение потока событий apiserver ---

// watchOnce открывает watch-соединение и читает события до обрыва потока
// (EOF, сетевая ошибка, истечение серверного таймаута watch). Возвращает
// ошибку — вызывающий код решает, переподключаться или нет.
func watchOnce(ctx context.Context, c *k8sClient) error {
	path := c.greetingsPath() + "?watch=true"
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return fmt.Errorf("открытие watch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("watch: статус %d: %s", resp.StatusCode, string(body))
	}

	log.Printf("watch: соединение установлено, слушаю события greetings в ns=%s", c.ns)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev watchEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			log.Printf("watch: не удалось разобрать событие: %v", err)
			continue
		}
		var g greeting
		if err := json.Unmarshal(ev.Object, &g); err != nil {
			log.Printf("watch: не удалось разобрать объект события %s: %v", ev.Type, err)
			continue
		}
		switch ev.Type {
		case "ADDED", "MODIFIED":
			log.Printf("watch: событие %s greeting=%s (spec.who=%q) — запускаю reconcile", ev.Type, g.Metadata.Name, g.Spec.Who)
			reconcile(ctx, c, g.Metadata.Name)
		case "DELETED":
			// Оператор подписан на Greeting, а не на порождённые объекты.
			// ConfigMap не удаляется намеренно: без ownerReferences нет
			// встроенной сборки мусора, а её реализация — за рамками этого
			// демо (см. README, раздел про watch/resync).
			log.Printf("watch: событие DELETED greeting=%s — ConfigMap greeting-%s не трогаю (нет ownerReferences, вне рамок демо)", g.Metadata.Name, g.Metadata.Name)
		default:
			log.Printf("watch: неизвестный тип события %q, объект=%s", ev.Type, g.Metadata.Name)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("чтение потока watch: %w", err)
	}
	return fmt.Errorf("поток watch закрыт сервером (EOF)")
}

// watchLoop переподключается после каждого обрыва watch с паузой, не роняя
// под.
func watchLoop(ctx context.Context, c *k8sClient) {
	for {
		if ctx.Err() != nil {
			return
		}
		err := watchOnce(ctx, c)
		if ctx.Err() != nil {
			return
		}
		log.Printf("watch: соединение прервано (%v) — переподключаюсь через %s", err, watchRetryWait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(watchRetryWait):
		}
	}
}

func main() {
	log.SetFlags(log.LstdFlags)
	log.Printf("greeter-operator: старт")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	c, err := newK8sClient()
	if err != nil {
		log.Fatalf("инициализация клиента Kubernetes API: %v", err)
	}
	log.Printf("greeter-operator: клиент готов, namespace=%s, api=%s", c.ns, c.base)

	// Первый проход сразу при старте — не ждать первого тика resync.
	list, err := c.listGreetings(ctx)
	if err != nil {
		log.Printf("стартовый проход: список Greeting не получен: %v", err)
	} else {
		log.Printf("стартовый проход: найдено Greeting: %d", len(list.Items))
		for _, item := range list.Items {
			reconcile(ctx, c, item.Metadata.Name)
		}
	}

	go resyncLoop(ctx, c)
	watchLoop(ctx, c)

	log.Printf("greeter-operator: остановка")
}
