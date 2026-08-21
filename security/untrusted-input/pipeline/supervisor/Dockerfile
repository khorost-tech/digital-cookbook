FROM golang:1.26.5-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/supervisor .

# Супервизор не открывает содержимое файлов, поэтому ffmpeg ему не нужен.
# Чем меньше в образе, тем меньше пригодится атакующему при компрометации.
FROM alpine:3.20.3
COPY --from=build /out/supervisor /usr/local/bin/supervisor

# Каталоги обмена с парсером: тома наследуют права из образа при первом
# монтировании, иначе процесс под непривилегированным uid в них не запишет.
RUN mkdir -p /data/jobs /data/done && chown -R 65534:65534 /data

ENTRYPOINT ["/usr/local/bin/supervisor"]
