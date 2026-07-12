FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 go build -o /app .

FROM alpine:3.20
COPY --from=build /app /app
# The default payload path is /payload/sample-request.json (see PAYLOAD env
# var). It is intentionally not baked into this image: the build context is
# limited to this directory, so mount the stand's
# topology/payload/sample-request.json as a volume at that path, or point
# PAYLOAD elsewhere. Without a mounted file the client falls back to a
# small built-in placeholder payload so it still runs standalone.
ENTRYPOINT ["/app"]
