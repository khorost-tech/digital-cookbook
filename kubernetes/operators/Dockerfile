# Продуктовый путь для greeter-operator.
#
# Живой стенд запускает оператор через `go run /src/main.go` из ConfigMap на
# стоковом golang:1.26 — это намеренное упрощение демо (не нужен registry, не
# нужна загрузка модулей, видна механика reconcile в одном файле). В проде
# такое не делают: собирают образ, как здесь.
#
# main.go — только стандартная библиотека, поэтому сборочный этап не тянет
# ни go.mod, ни зависимости из модульного прокси.

FROM golang:1.26 AS build
WORKDIR /src
COPY cmd/greeter-operator/main.go .
# CGO_ENABLED=0 — статический бинарь, без libc-зависимостей на рантайме,
# что и позволяет запускать его на distroless/static ниже.
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/greeter-operator main.go

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/greeter-operator /greeter-operator
USER nonroot:nonroot
ENTRYPOINT ["/greeter-operator"]
