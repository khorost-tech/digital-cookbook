FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 go build -o /app .

FROM alpine:3.20
COPY --from=build /app /app
ENTRYPOINT ["/app"]
