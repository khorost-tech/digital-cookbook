# Сборка: статический бинарь без cgo, пути сборки вычищены (-trimpath), без build id —
# два прогона на одном коммите дают один и тот же бинарь. Компилятор работает на платформе
# машины сборки и кросс-компилирует под целевую (--platform linux/amd64 для k8s-volga).
FROM --platform=$BUILDPLATFORM golang:1.27@sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG CMD=service TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -buildid=" -o /out/app ./cmd/${CMD}

# Рантайм: distroless static — CA-сертификаты, tzdata, пользователь nonroot, без шелла и пакетов.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
