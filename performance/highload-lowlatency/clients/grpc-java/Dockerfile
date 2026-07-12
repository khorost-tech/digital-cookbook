# Multi-stage build for the gRPC load-generating Java client.
#
# Like clients/grpc-go (and unlike clients/java, a self-contained REST
# client), this client generates its gRPC stubs at build time straight from
# the shared contract in ../../grpc/proto/check.proto (see pom.xml's
# protobuf-maven-plugin <protoSourceRoot>, which points there). That means
# the Docker build context must include BOTH grpc/proto and
# clients/grpc-java, so the build context for this Dockerfile is the
# highload-lowlatency stand root, not this directory — see
# topology/docker-compose.yml (`context: ..`, `dockerfile:
# clients/grpc-java/Dockerfile`), same layout as services/grpc-backend and
# clients/grpc-go.
FROM maven:3.9-eclipse-temurin-21 AS build
WORKDIR /src

# Copy the proto contract first (protoSourceRoot resolves to ../../grpc/proto
# relative to clients/grpc-java, i.e. /src/grpc/proto from here).
COPY grpc/proto ./grpc/proto
COPY clients/grpc-java/pom.xml ./clients/grpc-java/pom.xml

WORKDIR /src/clients/grpc-java
# Warm the local dependency/plugin cache (including the protoc/protoc-gen-grpc-java
# binaries os-maven-plugin resolves for this platform) in its own layer.
RUN mvn -q -B dependency:go-offline || true

COPY clients/grpc-java/src ./src
RUN mvn -q -B -DskipTests package

FROM eclipse-temurin:21-jre-alpine
WORKDIR /app
COPY --from=build /src/clients/grpc-java/target/highload-grpc-java-client.jar /app/app.jar
# Default payload path is /payload/sample-request.json (see PAYLOAD env var),
# the same ~8KB JSON body used by clients/go, clients/java and
# clients/grpc-go — it is re-packed into a CheckRequest proto at startup (see
# Config.buildRequest in Client.java). It is intentionally not baked into
# this image: mount the stand's topology/payload/sample-request.json as a
# volume at that path, or point PAYLOAD elsewhere. Without a mounted file the
# client falls back to a small built-in placeholder payload so it still runs
# standalone.
ENTRYPOINT ["java", "-jar", "/app/app.jar"]
