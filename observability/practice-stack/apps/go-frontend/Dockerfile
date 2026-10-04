# Версия Go не ниже указанной в go.mod: на хосте Go 1.26.3, и go mod init
# записывает go 1.26.3 — образ golang:1.26-alpine с этим совместим.
FROM golang:1.26-alpine AS build
WORKDIR /src
# GOPROXY задаётся аргументом, чтобы стенд собирался и за прокси автора, и
# у читателя с обычным proxy.golang.org.
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -o /go-frontend .

FROM alpine:3.22
RUN adduser -D -u 10001 app
USER app
COPY --from=build /go-frontend /usr/local/bin/go-frontend
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/go-frontend"]
