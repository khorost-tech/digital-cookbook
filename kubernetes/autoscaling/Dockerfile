# Dockerfile — продуктовый путь для читателя.
#
# Живой стенд НЕ собирает этот образ и не пушит его в registry: под metricgen
# запускает исходник из ConfigMap через `go run` на стоковом golang:1.26 (см.
# README, раздел «Почему go run из ConfigMap, а не образ»). Этот Dockerfile —
# то, как выглядит нормальная сборка сервиса в проде: multi-stage, статический
# бинарник, distroless-рантайм без шелла и пакетного менеджера.

FROM golang:1.26 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /metricgen ./cmd/metricgen

FROM gcr.io/distroless/static:nonroot
COPY --from=build /metricgen /metricgen
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/metricgen"]
