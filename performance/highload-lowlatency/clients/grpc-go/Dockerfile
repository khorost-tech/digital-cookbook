# Multi-stage build for the gRPC load-generating client.
#
# Like services/grpc-backend (and unlike clients/go, which is a
# single-module, self-contained build context), this client imports the
# generated gRPC code from ../../grpc/gen as a separate Go module via a
# `replace` directive to a relative path (see go.mod). That means the Docker
# build context must include BOTH clients/grpc-go and grpc/gen, so the build
# context for this Dockerfile is the highload-lowlatency stand root, not this
# directory — see topology/docker-compose.yml (`context: ..`, `dockerfile:
# clients/grpc-go/Dockerfile`).
FROM golang:1.23-alpine AS build
WORKDIR /src

# Copy both modules before `go mod download` so the relative replace
# directive in clients/grpc-go/go.mod resolves during the build.
COPY grpc/gen ./grpc/gen
COPY clients/grpc-go ./clients/grpc-go

WORKDIR /src/clients/grpc-go
RUN go mod download
RUN CGO_ENABLED=0 go build -o /app .

FROM alpine:3.20
COPY --from=build /app /app
# The default payload path is /payload/sample-request.json (see PAYLOAD env
# var), the same ~8KB JSON body used by clients/go and clients/java — it is
# re-packed into a CheckRequest proto at startup (see decodeCheckRequest in
# main.go). It is intentionally not baked into this image: mount the stand's
# topology/payload/sample-request.json as a volume at that path, or point
# PAYLOAD elsewhere. Without a mounted file the client falls back to a small
# built-in placeholder payload so it still runs standalone.
ENTRYPOINT ["/app"]
