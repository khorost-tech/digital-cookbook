# Multi-stage build for the gRPC reference backend.
#
# Unlike services/go-backend (single-module, self-contained build context),
# this service imports the generated gRPC code from ../../grpc/gen as a
# separate Go module via a `replace` directive to a relative path (see
# go.mod). That means the Docker build context must include BOTH
# services/grpc-backend and grpc/gen, so the build context for this
# Dockerfile is the highload-lowlatency stand root, not this directory —
# see topology/docker-compose.yml (`context: .. , dockerfile:
# services/grpc-backend/Dockerfile`).
FROM golang:1.23-alpine AS build
WORKDIR /src

# Copy both modules before `go mod download` so the relative replace
# directive in services/grpc-backend/go.mod resolves during the build.
COPY grpc/gen ./grpc/gen
COPY services/grpc-backend ./services/grpc-backend

WORKDIR /src/services/grpc-backend
RUN go mod download
RUN CGO_ENABLED=0 go build -o /app .

FROM alpine:3.20
COPY --from=build /app /app
EXPOSE 9100
ENTRYPOINT ["/app"]
