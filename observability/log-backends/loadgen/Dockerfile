# Версия Go совпадает с go.mod: несовпадение даёт отказ сборки, который на
# первый взгляд выглядит как ошибка кода.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -o /out/loadgen .

FROM alpine:3.21
COPY --from=build /out/loadgen /usr/local/bin/loadgen
ENTRYPOINT ["loadgen"]
