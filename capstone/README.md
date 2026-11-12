# Capstone — примеры

Сквозные стенды: один сервис от кода до прода — конфигурация, события, сборка и поставка, кластер, наблюдаемость и нагрузка.

| Стенд | Описание | Статья |
|---|---|---|
| [`shortlinks/`](shortlinks/) | Сервис коротких ссылок (Go) и consumer аналитики (Java) от кода до кластера: валидация конфигурации, graceful shutdown, singleflight против залпа промахов кэша; брокер недоступен, outbox против прямой публикации, идемпотентный consumer при падении до ack; distroless, воспроизводимая сборка, SBOM fat-jar, подпись cosign, Trivy; выкатка без потерь, KEDA по лагу JetStream и self-heal ArgoCD на k8s-volga; трейс Go → NATS → Java, burn-rate SLO и насыщение пула соединений под нагрузкой | [статья](https://khorost.tech/capstone/capstone-service-overview-and-code/) |

---

Навигация: [все категории](../README.md) · [полный список примеров](../INDEX.md)
