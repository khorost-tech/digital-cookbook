# production-patterns

Стенд к статье №8 (java-deep-dive): **resilience4j (circuit breaker / retry / rate
limiter / bulkhead), graceful shutdown, health/readiness** на нестабильном downstream.
Plain-Java (без Spring Boot — resilience4j-core API вызывается напрямую, без
autoconfigure, так виден реальный порядок декораторов). resilience4j **2.3.0** (parent BOM).

## Что внутри

- `UnstableDownstream` — заглушка внешней системы, две фазы: `outage` (первые N мс —
  100% отказ) и `recovered` (дальше — 8% остаточных ошибок + сетевая задержка
  40–240 мс). Обе фазы реально спят (`Thread.sleep`), не мгновенны.
- `ResilienceStack` — цепочка декораторов **Retry → CircuitBreaker → RateLimiter →
  Bulkhead → downstream** (снаружи внутрь): каждая retry-попытка заново проходит через
  CB/RateLimiter/Bulkhead, как в реальном клиенте.
  - CircuitBreaker: `slidingWindow=10 (count-based)`, `minimumNumberOfCalls=5`,
    `failureRateThreshold=50%`, `waitDurationInOpenState=4s`,
    `permittedCallsInHalfOpen=3`, `automaticTransitionToHalfOpen=true`. Игнорирует
    `RequestNotPermitted`/`BulkheadFullException` в статистике — перегрузка нашего же
    клиента не должна маскироваться под деградацию downstream.
  - Retry: `maxAttempts=3`, экспоненциальный backoff `300ms × 2.0`. Игнорирует
    `CallNotPermittedException` — не долбить открытый breaker.
  - RateLimiter: `5 вызовов / 1с`, `timeoutDuration=0` (мгновенный отказ сверх лимита).
  - Bulkhead: `maxConcurrentCalls=3`, `maxWaitDuration=0` (мгновенный отказ сверх лимита).
- `HealthServer` — свой liveness/readiness на `com.sun.net.httpserver` (JDK, без
  доп. зависимостей): `/health/live` всегда 200 пока жив процесс, `/health/ready` —
  503 во время graceful drain.
- `Main` — нагрузка: новая задача каждые 150 мс в пул из 8 воркеров. Shutdown hook на
  SIGTERM: readiness→false, новые задачи не принимаются, `ExecutorService.shutdown()` +
  `awaitTermination()` ждёт завершения in-flight вызовов до выхода.

## Сборка

Хостового Maven нет — только через Docker (JDK 25 внутри образа):

```bash
cd java-deep-dive
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$(pwd)/..:/app" -v "$HOME/.m2:/root/.m2" \
  -w /app/java-deep-dive maven:3.9-eclipse-temurin-25 \
  mvn -q -pl production-patterns -am package
```

Собирает shaded-jar `production-patterns/target/production-patterns.jar`
(Main-Class: `tech.khorost.productionpatterns.Main`).

## Прогон

```bash
MSYS_NO_PATHCONV=1 docker run -d --name pp-demo -p 18080:8080 \
  -v "$(pwd)/production-patterns/target:/app" eclipse-temurin:25-jdk \
  java -jar /app/production-patterns.jar 5000 8080 150
#                                          ^^^^ ^^^^ ^^^
#                            recoveryAfterMillis health submitIntervalMillis
```

Health: `curl http://localhost:18080/health/live`, `/health/ready`.

Graceful shutdown: `docker stop -t 20 pp-demo` (SIGTERM, ждёт до 20с перед SIGKILL).

## Реальный вывод (прогон 2026-07-07/08, контейнер `eclipse-temurin:25-jdk`)

Один непрерывный прогон (`docker run -d ... ; docker stop -t 20 pp-demo`) — все
примеры ниже (CB-цикл, retry, rate limiter, bulkhead, graceful shutdown) взяты из
одного и того же лога, дословно, без склейки между разными запусками.

### Circuit breaker: полный цикл CLOSED → OPEN → HALF_OPEN → (OPEN) → HALF_OPEN → CLOSED

```
20:57:17.686 [work] WARN >>> [CircuitBreaker] CLOSED -> OPEN at 2026-07-07T20:57:17.686769202Z
20:57:22.088 [work] WARN >>> [CircuitBreaker] OPEN -> HALF_OPEN at 2026-07-07T20:57:22.088210245Z
20:57:22.477 [work] WARN >>> [CircuitBreaker] HALF_OPEN -> OPEN at 2026-07-07T20:57:22.477823046Z
20:57:26.478 [CircuitBreakerAutoTransitionThread] WARN >>> [CircuitBreaker] OPEN -> HALF_OPEN at 2026-07-07T20:57:26.478189032Z
20:57:27.034 [work] WARN >>> [CircuitBreaker] HALF_OPEN -> CLOSED at 2026-07-07T20:57:27.034011170Z
```

CB открылся через ~500мс после старта (5 вызовов подряд упали в "outage"-фазе,
`failureRate=100%`). Первый переход в HALF_OPEN (t=22.1s, через `waitDurationInOpenState=4s`)
попал ровно на границу восстановления downstream (`recoveryAfterMillis=5000`, пробный
вызов в half-open пришёлся на ещё не восстановившийся downstream) — пробный вызов упал,
breaker честно вернулся в OPEN. Второй HALF_OPEN (t=26.5s) пришёлся уже на реально
восстановившийся downstream — пробные вызовы прошли, breaker закрылся. Это не баг
демо, а реалистичный edge-case: breaker не отличает "downstream ещё падает" от
"downstream только что ожил", он просто пробует и реагирует на факт.

### Retry: попытки с экспоненциальным backoff, атрибутированные к задаче

```
20:57:17.255 [work] INFO >>> [Retry] [task-1] attempt #1 after: downstream unavailable (call #1, outage, elapsed=189ms)
20:57:17.381 [work] INFO >>> [Retry] [task-2] attempt #1 after: downstream unavailable (call #2, outage, elapsed=312ms)
20:57:17.653 [work] INFO >>> [Retry] [task-1] attempt #2 after: downstream unavailable (call #4, outage, elapsed=581ms)
20:57:17.684 [work] INFO >>> [Retry] [task-2] attempt #2 after: RateLimiter 'downstream' does not permit further calls
```

Ретрай реально ждёт между попытками (300мс → 600мс) и игнорирует
`CallNotPermittedException`, когда breaker уже открыт (не молотит впустую). Все
worker-потоки в пуле называются одинаково (`"work"`), поэтому у общего на всё
приложение `Retry`-объекта (одна цепочка декораторов на все задачи) событие `onRetry`
само по себе не знает, какой задаче принадлежит попытка — id задачи пробрасывается
через `ThreadLocal` (`ResilienceStack.currentTaskId`, выставляется в `Main` вокруг
`decorated.get()`), поэтому в логе видно `[task-N]`, а не голое `attempt #N`. Так
собирается "путь одного запроса": `[task-1] start` → `[Retry] [task-1] attempt #1` →
`[Retry] [task-1] attempt #2` → `[task-1] OK/failed`.

### Rate limiter: отказы сверх лимита (5/1с)

За прогон — **738** отклонённых вызовов с `[RateLimiter] rejected — limit exhausted
(5/1s)` (сырые события — каждая проверка лимита, включая внутри одной retry-попытки),
регулярно видно в `[state]`-сводке: `availablePermissions` падает до 2–3 в пике
нагрузки и восстанавливается до 5 на границе секунды.

Это **не то же самое**, что `rlRejected` в сводке `drain completed` (см. ниже) —
там 202: это только **окончательные** отказы, когда задача исчерпала все 3 попытки
retry и так и не дождалась свободного лимита. Разница (738 сырых событий против
202 окончательных отказов) — это ретраи, которые в итоге всё-таки прошли лимитер
на второй/третьей попытке. 738 — счётчик нагрузки на лимитер, 202 — счётчик
провалившихся для клиента задач; путать их — читать как баг то, что им не является.

### Bulkhead: отказы сверх конкурентности (3 одновременных)

За прогон — **33** сырых отклонения вида `[Bulkhead] rejected — 3 concurrent slots
full`, из них **10** окончательных (`bhRejected` в сводке `drain completed`) — та же
логика "сырое событие на попытку" vs "окончательный отказ задачи", что и у rate
limiter. Пример:

```
20:57:28.387 [work] WARN >>> [Bulkhead] rejected — 3 concurrent slots full
20:57:30.486 [work] WARN >>> [Bulkhead] rejected — 3 concurrent slots full
```

### Graceful shutdown: drain in-flight на SIGTERM (`docker stop`)

```
20:59:03.999 [work] INFO [task-629] start
20:59:04.148 [work] INFO >>> [Retry] [task-629] attempt #1 after: downstream transient error (call #418, recovered, elapsed=94361ms)
20:59:04.299 [work] INFO [task-631] start
20:59:04.300 [work] INFO >>> [Retry] [task-631] attempt #1 after: RateLimiter 'downstream' does not permit further calls
20:59:04.448 [work] INFO >>> [Retry] [task-629] attempt #2 after: RateLimiter 'downstream' does not permit further calls
20:59:04.449 [work] INFO [task-632] start
20:59:04.449 [work] INFO >>> [Retry] [task-632] attempt #1 after: RateLimiter 'downstream' does not permit further calls
20:59:04.495 [shutdown-hook] WARN === SIGTERM received at 2026-07-07T20:59:04.493717580Z — starting graceful shutdown ===
20:59:04.495 [shutdown-hook] WARN [Health] readiness = false
20:59:04.600 [work] INFO >>> [Retry] [task-631] attempt #2 after: RateLimiter 'downstream' does not permit further calls
20:59:04.806 [work] INFO [task-632] OK: OK#421 (356ms)
20:59:05.107 [work] INFO [task-629] OK: OK#422 (1107ms)
20:59:05.369 [work] INFO [task-631] OK: OK#423 (1069ms)
20:59:05.370 [shutdown-hook] WARN === drain completed in 876ms (submitted=632, succeeded=377, failed=255, cbRejected=42, rlRejected=202, bhRejected=10) ===
20:59:05.370 [shutdown-hook] WARN === shutdown finished at 2026-07-07T20:59:05.370624990Z (cbState=CLOSED) ===
```

Три задачи стартовали **до** SIGTERM (20:59:04.495) и были in-flight в момент
сигнала: `task-629` (start 03.999, шёл на 2-ю retry-попытку), `task-631` (start
04.299, тоже на 2-й попытке) и `task-632` (start 04.449, 1-я попытка). Все три
завершились успехом **после** сигнала — shutdown hook дождался их через
`ExecutorService.awaitTermination()`, не оборвал.

Числа в скобках — это `Duration.between(start, Instant.now())`, посчитанная в
коде на реальных `Instant`, а не декоративная: `task-632` — `04.806 − 04.449 =
357ms` (лог: 356мс), `task-629` — `05.107 − 03.999 = 1108ms` (лог: 1107мс),
`task-631` — `05.369 − 04.299 = 1070ms` (лог: 1069мс). Разница в 1мс — обычное
округление между моментом вычисления `Duration` и моментом печати строки
логгером, не более того. Длительность **включает** все retry-попытки и
backoff-паузы между ними (замер идёт от `[task-N] start` до финального
результата, а не только от последней попытки) — поэтому у `task-629`/`task-631`
она больше секунды, хотя каждый отдельный вызов downstream быстрее.

Readiness упал в `false` немедленно при получении сигнала (до завершения drain) —
в реальной системе это даёт балансировщику время вывести под из ротации, пока
in-flight запросы дорабатывают. Контейнер вышел с кодом 143 (128+SIGTERM —
штатное поведение JVM при обработке сигнала через встроенный signal handler, не
форс-килл: `docker stop -t 20` не понадобилось ждать таймаут, drain уложился в
876мс).
