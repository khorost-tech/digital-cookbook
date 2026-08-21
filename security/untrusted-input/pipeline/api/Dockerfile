# Образы пиновать по патч-версии: плавающий тег завтра приедет другой сборкой,
# и стенд перестанет соответствовать статье.
FROM golang:1.26.5-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/api .

# API не разбирает содержимое файлов, поэтому ему не нужен ffmpeg — и вообще
# почти ничего. Минимальная база уменьшает то, что можно использовать при
# компрометации.
FROM alpine:3.20.3
RUN adduser -D -u 65532 api

# Каталог создаётся в образе с нужным владельцем: именованный том Docker при
# первом монтировании наследует права каталога из образа. Иначе том появится
# принадлежащим root, а сервис под непривилегированным пользователем не сможет
# в него писать — и приём файлов будет падать с невнятной ошибкой.
RUN mkdir -p /data/input && chown -R 65532:65532 /data

COPY --from=build /out/api /usr/local/bin/api
USER api
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
