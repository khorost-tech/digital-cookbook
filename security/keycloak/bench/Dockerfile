# Нагрузчик для стенда (Task 7). Собирается и запускается ВНУТРИ docker-сети
# стенда (сервис `bench`, профиль bench в docker-compose.yml), чтобы мерить путь
# resource server ↔ Keycloak напрямую (backend-go:8081, keycloak:8080), а не через
# NAT Docker Desktop на Windows — иначе latency и стабильность искажает host-проксирование.
FROM golang:1.25-alpine AS build
WORKDIR /src
# Зависимостей нет (только stdlib) — go.mod без require, кэш модулей не нужен.
COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/bench .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/bench /bench
ENTRYPOINT ["/bench"]
