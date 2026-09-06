# Multi-stage build для Go OIDC resource server.
# Версия golang >= go.mod (go 1.25). Кэш-прокси khorost для воспроизводимости.
FROM golang:1.26-alpine AS build
ENV GOPROXY=https://go.khorost.tech,direct
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app .

# Минимальный финальный образ, non-root.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates \
    && adduser -D -u 10001 app
USER app
COPY --from=build /app /app
EXPOSE 8081
ENTRYPOINT ["/app"]
