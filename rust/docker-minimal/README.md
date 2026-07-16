# Стенд: Rust в Docker — минимальные образы и cross-compilation

Companion к статье [«Rust в Docker: минимальные образы и cross-compilation»](https://khorost.tech/rust/rust-docker-minimal-images/).

Минимальный HTTP-сервис на Axum (один эндпоинт `GET /health` → `ok`) — смысл демо не в
сервисе, а в трёх Dockerfile'ах: как упаковать Rust-бинарник в образ размером меньше мегабайта
и как кросс-собрать его под ARM64 с x86_64-хоста.

## Prerequisites

- Docker с включённым BuildKit (в актуальном Docker уже по умолчанию).
- Для cross-сборки под ARM64 (`Dockerfile.cross`) — `docker buildx` (входит в Docker Desktop /
  свежий Docker Engine). QEMU для эмуляции **не нужен**: код кросс-компилируется на x86_64
  через cargo-zigbuild, финальная стадия — только `COPY`.
- Локальный Rust-тулчейн не требуется — всё собирается внутри образов.

## Варианты сборки

### 1. musl-статика в scratch (самый маленький)

```bash
docker build -t rust-app:musl .
docker run --rm -p 8080:8080 rust-app:musl
curl -s localhost:8080/health        # -> ok
```

cargo-chef кеширует слой зависимостей отдельно от кода: пока `Cargo.lock` не менялся,
пересобирается только ваш код.

### 2. glibc в distroless/cc (компромисс без musl-нюансов)

```bash
docker build -f Dockerfile.distroless -t rust-app:distroless .
docker run --rm -p 8080:8080 rust-app:distroless
curl -s localhost:8080/health        # -> ok
```

### 3. Cross-compilation под ARM64 (с x86_64-хоста)

```bash
# ОБЯЗАТЕЛЬНО buildx с явной целевой платформой — иначе итоговый образ получит
# архитектуру ХОСТА (amd64) при arm64-бинарнике внутри:
docker buildx build --platform linux/arm64 -f Dockerfile.cross -t rust-app:arm64 --load .
docker image inspect rust-app:arm64 --format '{{.Architecture}}'   # -> arm64
```

Запустить arm64-образ на x86_64-хосте можно через QEMU (`docker run --platform linux/arm64 …`,
требует `binfmt`), но штатное назначение — деплой на ARM64 (Graviton/Ampere).

## Размеры образов

Замерено `docker image inspect --format '{{.Size}}'` (десятичные МБ, как в `docker images`):

| Образ | Dockerfile | Размер |
|-------|-----------|--------|
| musl-статика в `scratch` | `Dockerfile` | **0.53 МБ** |
| glibc в `distroless/cc` | `Dockerfile.distroless` | **9.5 МБ** |

Разница — цена удобства: distroless тащит glibc + libgcc + CA-серты, зато без musl-нюансов
линковки. Оба на порядки меньше типового `FROM rust` (~1.5 ГБ со всем тулчейном).

## Что где

| Файл | Назначение |
|------|-----------|
| `src/main.rs` | Axum-сервис, `GET /health` на `:8080` |
| `Cargo.toml` | зависимости + release-профиль под минимальный размер (strip/lto/opt-level=z) |
| `Dockerfile` | cargo-chef + musl-статика в `scratch` |
| `Dockerfile.distroless` | glibc в `distroless/cc` |
| `Dockerfile.cross` | cross-compilation под aarch64 через cargo-zigbuild |

## Проверка

```bash
curl -s localhost:8080/health   # ok
```
