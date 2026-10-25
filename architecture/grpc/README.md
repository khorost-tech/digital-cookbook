# architecture/grpc — gRPC за пределами unary-вызова, счётом исходов

Стенд к статье [«gRPC вглубь: стриминг, deadlines и interceptors»](https://khorost.tech/architecture/grpc-deep-dive/).
Один `.proto`, сервер и клиент-проба на Go (grpc-go 1.84) и на Java (grpc-java
1.84) — все четыре пары клиент-сервер, — gRPC-Gateway 2.31, Angie 1.12 и Envoy
1.39 как L4 и как L7. Каждая ось считает **исход**: бросило ли нижнее звено
работу, остановился ли стрим, какой код увидел клиент, сколько попыток дошло
до сервера, пережило ли соединение простой, на каком бэкенде оказались вызовы.
Задержки и пропускная способность не публикуются.

## Что внутри

```
proto/        demo.proto (Unary, три вида стримов, звено Chain), gateway.yaml,
              gen.sh — генерация Go-кода на закреплённых protoc 36.2 и плагинах
go/           gen/ (сгенерировано), cmd/server, cmd/client (проба), cmd/gateway,
              cmd/gateway-details (тот же шлюз + пустой импорт errdetails)
java/         Server и Client (проба) на grpc-java; код из proto — при сборке
angie/        l4.conf (stream) и l7.conf (grpc_pass) — вместо angie.conf целиком
envoy/        l4.yaml (tcp_proxy) и l7.yaml (http_connection_manager)
scripts/      раннеры осей, разборы с контролями, проверки, реестр диверсий
fixtures/     замеры *.jsonl; manifests/ — сводки сессий; RUN-COMPLETE — маркер
expected.json ожидания, записанные до прогона
images.json   сторонние образы по дайджесту
```

Поведение серверов задают поля запроса и переменные `GRPC_*` (список — в
начале `go/cmd/server/main.go`): одна сборка ставит все опыты. Что Go и Java
ведут себя одинаково по умолчанию, проверяет `fixtures/protocol.jsonl` — один
набор вызовов во всех четырёх парах.

## Как прогнать

Нужны Docker и Python 3.8+; Go, Java и Maven — в образах.

```sh
bash scripts/run-all.sh                                   # всё разом, маркер последним
```

Один вариант — одной командой (её показывает статья):

```sh
python3 scripts/run-deadline.py go java go fresh 300      # ось 1: клиент → A → B
python3 scripts/run-cancel.py go java no-check            # ось 2: отмена стрима
python3 scripts/run-cancel.py go java async               # ось 2: Java, отправка после возврата обработчика
python3 scripts/run-errors.py go go errors panic          # ось 3: паника без перехватчика
python3 scripts/run-errors.py go gateway details          # ось 8: детали через шлюз
python3 scripts/run-chain.py go go metrics-first          # ось 4: порядок интерсепторов
python3 scripts/run-chain.py go go recover-outer          # ось 4: паника и метрики
python3 scripts/run-retry.py java java max10              # ось 5: ретраи service config
python3 scripts/run-retry.py go go stream-after           # ось 5: стрим после первого сообщения
python3 scripts/run-keepalive.py go go pings default      # ось 6: too_many_pings
python3 scripts/run-keepalive.py go idle envoy-l7 on      # ось 6: простой за прокси
python3 scripts/run-keepalive.py go silent angie-l4 off   # ось 6: молчащий стрим за прокси
python3 scripts/run-balance.py go go angie-l4             # ось 7: балансировка
python3 scripts/run-size.py java go 5mb                   # ось 9: размер ответа
```

Проверки и диверсии:

```sh
python3 -m unittest discover -s scripts -p "test_*.py"
python3 scripts/sabotage.py         # каждая проверка обязана покраснеть от своей поломки
```

## Что замерено

Подробности и сырые выводы — в `fixtures/`; две сессии полного прогона совпали
по опубликованным исходам (`fixtures/manifests/`).

**Ось 1 — deadline в цепочке** (`deadline.jsonl`). Клиент → A → B, срок 300 мс,
работа B — 10 шагов по 100 мс. Если A передаёт свой контекст, B узнаёт об
отмене и бросает работу во всех восьми сочетаниях Go и Java; если A создаёт
новый (Go — `context.Background()`, Java — `Context.ROOT`), B доделывает все
шаги, хотя клиент давно получил DEADLINE_EXCEEDED.

**Ось 2 — отмена стрима** (`cancel.jsonl`). Отмену останавливает только явная
проверка в цикле сервера. Без неё цикл идёт до конца: grpc-go возвращает
ошибку из `Send`. В grpc-java ответ зависит от того, где идёт отправка: цикл
внутри обработчика вызова — `onNext` возвращается молча, потому что
уведомление об отмене (`onCancel`) ждёт возврата обработчика; цикл в отдельной
задаче после возврата — `onNext` бросает CANCELLED; с `setOnCancelHandler`
исключения нет, а сам обработчик отмены вызывается до конца цикла только во
втором случае. `isCancelled()` видит отмену везде.

**Оси 3 и 8 — ошибки и gRPC-Gateway** (`errors.jsonl`). Обычная ошибка языка —
UNKNOWN; `status` — свой код; детали `ErrorInfo` доходят до клиента любого
языка. Паника без перехватчика роняет сервер Go целиком (клиент видит
UNAVAILABLE), исключение в Java даёт UNKNOWN, и сервер жив; с перехватчиком —
INTERNAL у обоих. Через шлюз: NotFound — 404, InvalidArgument — 400,
Unavailable — 503, Unknown и Internal — 500, `Grpc-Timeout` доходит до сервера
(504). Ошибка с деталями — 500, пока в бинарнике шлюза не зарегистрирован тип
`google.rpc.ErrorInfo`; с пустым импортом `errdetails` — 404 и детали в теле.

**Ось 4 — порядок интерсепторов** (`chain.jsonl`). Авторизация первой — метрики
и журнал не видят отклонённых вызовов; метрики первыми — видят все и считают
их кодом UNAUTHENTICATED. Обработчик в обоих порядках получает только вызовы с
токеном. Перехватчик паники снаружи метрик: клиент получает INTERNAL, а метрики
паники не видят; перехватчик внутри, ближе к обработчику, — метрики считают
вызов кодом INTERNAL. В Go стенд собирает только unary-цепочку
(`ChainUnaryInterceptor`); стримы её не проходят. Java-перехватчик стенда
оборачивает `onHalfClose` — там вызывается обработчик unary и серверного
стрима; исключения из `onMessage` клиентского и двунаправленного стрима он не
ловит.

**Ось 5 — встроенные ретраи** (`retry.jsonl`). Повторяются только коды из
`retryableStatusCodes`; `maxAttempts` больше 5 урезается до 5 (предел по
умолчанию, в Go меняется `WithMaxCallAttempts`); повторы делает сам gRPC (номер
попытки — `grpc-previous-rpc-attempts`). Пауза перед повтором — случайная в
пределах 0,8–1,2 × `initialBackoff`, поэтому вызов со сроком 500 мс при паузе
около 1 с доходит до сервера ровно один раз. Вызов фиксируют заголовки ответа:
серверный стрим, упавший без заголовков, повторяется; отправивший заголовки и
затем UNAVAILABLE без единого сообщения — уже нет; упавший после первого
сообщения — тоже нет.

**Ось 6 — keepalive** (`keepalive.jsonl`). Клиенты Go и Java не пингуют чаще
раза в 10 с. При политике сервера по умолчанию (не чаще раза в 5 минут)
молчащий стрим получает GOAWAY too_many_pings; клиент Go видит его как
UNAVAILABLE, Java — как RESOURCE_EXHAUSTED. Стрим, по которому сервер шлёт
данные, штрафов не получает: сервер сбрасывает их, когда пишет сам.

Два разных простоя за прокси с таймаутом 15 с. Пережило ли соединение простой,
проба решает по локальному адресу TCP-соединения клиента (Go — `peer.LocalAddr`,
Java — `Grpc.TRANSPORT_ATTR_LOCAL_ADDR`), а не по состоянию канала: READY
бывает и после переподключения. Соединение без вызовов дольше
таймаута закрывается и за L4, и за L7; пинги клиента сохраняют его только за
L4 — у Angie `grpc_pass` и Envoy HTTP таймаут простоя соединения считает
активные вызовы, а не трафик. Открытый стрим, молчащий 20 с, наоборот,
переживает L7 и без пингов (у Envoy молчание внутри вызова ограничивает
отдельный `stream_idle_timeout`, по умолчанию 5 минут), а за L4 обрывается без
пингов и доживает с ними.

**Ось 7 — балансировка** (`balance.jsonl`). Один канал клиента, три бэкенда: за
L4 (Angie stream, Envoy TCP) и с `pick_first` все вызовы на одном бэкенде; за
L7 (Angie `grpc_pass`, Envoy HTTP) и с клиентским `round_robin` по DNS — на
всех трёх.

**Ось 9 — размер** (`size.jsonl`). Ответ 5 МБ при пределе приёма по умолчанию
(4 МБ) — RESOURCE_EXHAUSTED у Go и Java; с поднятым пределом — доходит.

## Чего эти числа не значат

- **Ничего о задержке и пропускной способности.** Сроки и паузы — постановка.
- **Одна машина:** macOS arm64, Docker Desktop, сеть — мост Docker.
- **Версии:** grpc-go и grpc-java 1.84, protoc 36.2, grpc-gateway 2.31, Angie
  1.12.2, Envoy 1.39. Минимальный интервал пингов, предел `maxAttempts`,
  отображение кодов в HTTP — свойства этих версий.
- **Прокси — в конфигурации стенда** с таймаутами простоя 15 с; у реальных
  балансировщиков облаков свои таймауты и своя реакция на пинги.
