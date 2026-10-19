# Образ приложения уровня B. Версия схемы вшивается через ldflags на этапе
# сборки: SCHEMA=old -> тег v1, SCHEMA=dual -> тег dual (промежуточная версия:
# пишет обе колонки, читает переходным COALESCE), SCHEMA=new -> тег v2.
# Один Dockerfile, три тега.
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG SCHEMA=old
RUN CGO_ENABLED=0 go build -ldflags "-X main.Schema=${SCHEMA}" -o /out/app .

FROM alpine:3.21
COPY --from=builder /out/app /app
EXPOSE 8080
ENTRYPOINT ["/app"]
