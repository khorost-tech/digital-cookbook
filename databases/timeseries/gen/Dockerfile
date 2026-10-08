# Сборка генератора в контейнере: Go на хосте читателю не нужен.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -o /gen .

FROM alpine:3.22
COPY --from=build /gen /usr/local/bin/gen
ENTRYPOINT ["gen"]
