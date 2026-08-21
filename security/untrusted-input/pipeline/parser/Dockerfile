FROM golang:1.26.5-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go probes.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/parser .

# Парсеру ffmpeg нужен — он и есть тот код, который разбирает недоверенные
# байты. Версия фиксируется: это компонент, ради изоляции которого построен
# весь конвейер. Свежий тег, к слову, не гарантирует наличия конкретного
# фикса — об этом первая статья серии.
FROM alpine:3.20.3
RUN apk add --no-cache ffmpeg
COPY --from=build /out/parser /usr/local/bin/parser

RUN mkdir -p /data/jobs /data/done && chown -R 65534:65534 /data

# Пользователь задаётся в compose (65534:65534, nobody).
ENTRYPOINT ["/usr/local/bin/parser"]
