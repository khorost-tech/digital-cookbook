# Сборка bench в контейнере: Go на хосте читателю не нужен.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -o /bench .

FROM alpine:3.20
COPY --from=build /bench /usr/local/bin/bench
ENTRYPOINT ["bench"]
