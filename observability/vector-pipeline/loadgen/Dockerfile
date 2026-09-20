# версия Go не ниже указанной в go.mod (на хосте Go 1.26.3, go mod init пишет go 1.26.x)
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN go build -o /loadgen .

FROM alpine:3.22
COPY --from=build /loadgen /usr/local/bin/loadgen
ENTRYPOINT ["/usr/local/bin/loadgen"]
