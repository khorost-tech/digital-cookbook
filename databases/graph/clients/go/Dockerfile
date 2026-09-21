# Go-клиент стенда: подключается к neo4j (bolt) и postgres (AGE + baseline).
# GOPROXY пробрасывается build-arg'ом — во внутренней сети автора это go.khorost.tech,
# публично можно оставить дефолт.
FROM golang:1.26-alpine AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /graph-client .

FROM alpine:3.20
RUN adduser -D -u 10001 app
USER app
COPY --from=build /graph-client /graph-client
ENTRYPOINT ["/graph-client"]
