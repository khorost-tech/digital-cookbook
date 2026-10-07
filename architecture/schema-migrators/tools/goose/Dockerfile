# goose собирается из исходников заданной версии: Go на хосте не нужен.
FROM golang:1.26-alpine AS build
ARG GOOSE_VERSION=v3.28.0
RUN CGO_ENABLED=0 go install github.com/pressly/goose/v3/cmd/goose@${GOOSE_VERSION}

FROM alpine:3.20
COPY --from=build /go/bin/goose /usr/local/bin/goose
ENTRYPOINT ["goose"]
