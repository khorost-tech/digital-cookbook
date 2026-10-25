#!/usr/bin/env bash
#
# gen.sh — генерация Go-кода из proto/ в go/gen/ на закреплённых версиях, в
# контейнере: читателю не нужен свой protoc. Java генерирует код сама при
# сборке (protobuf-maven-plugin в java/pom.xml).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
PROTOC=36.2
docker run --rm -v "$PWD:/w" -w /w golang:1.27.1-alpine sh -euc "
  arch=\$(uname -m); [ \"\$arch\" = aarch64 ] && arch=aarch_64
  for i in 1 2 3 4 5; do  # wget и unzip — из busybox образа; сеть Docker Desktop рвётся
    wget -q -O /tmp/protoc.zip https://github.com/protocolbuffers/protobuf/releases/download/v$PROTOC/protoc-$PROTOC-linux-\$arch.zip && break
    sleep 3
  done
  unzip -q /tmp/protoc.zip -d /opt/protoc
  for i in 1 2 3 4 5; do
    go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12 &&
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2 &&
    go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@v2.31.0 && break
    sleep 3
  done
  mkdir -p go/gen/demov1
  /opt/protoc/bin/protoc -I proto \
    --go_out=go/gen/demov1 --go_opt=paths=source_relative \
    --go-grpc_out=go/gen/demov1 --go-grpc_opt=paths=source_relative \
    --grpc-gateway_out=go/gen/demov1 --grpc-gateway_opt=paths=source_relative \
    --grpc-gateway_opt=grpc_api_configuration=proto/gateway.yaml \
    proto/demo.proto
  /opt/protoc/bin/protoc --version
"
