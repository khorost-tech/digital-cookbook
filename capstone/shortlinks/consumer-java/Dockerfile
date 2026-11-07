# Сборка: Maven с Temurin 25, зависимости отдельным слоем (кеш), fat-jar и Java-агент OpenTelemetry.
# jar от платформы не зависит: Maven работает на платформе машины сборки, без эмуляции.
FROM --platform=$BUILDPLATFORM maven:3.9-eclipse-temurin-25@sha256:93b8a14ea2f412782e4e842651273b4d903e35cc496284f178fbbe2d67d00976 AS build
WORKDIR /src
COPY pom.xml .
RUN mvn -q -B dependency:go-offline
COPY src src
RUN mvn -q -B package -DskipTests

# Рантайм: distroless Java 25 — JRE, nonroot, без шелла. Агент подключён всегда, включается
# переменной OTEL_JAVAAGENT_ENABLED (по умолчанию выключен).
FROM gcr.io/distroless/java25-debian13:nonroot@sha256:ca60da1345c0f17b6d019049e6749e15f10fd3c0da86dec938d2b4ec565d0629
COPY --from=build /src/target/consumer.jar /app/consumer.jar
COPY --from=build /src/target/opentelemetry-javaagent.jar /app/opentelemetry-javaagent.jar
ENV OTEL_JAVAAGENT_ENABLED=false OTEL_SERVICE_NAME=shortlinks-consumer
USER nonroot:nonroot
ENTRYPOINT ["java", "-javaagent:/app/opentelemetry-javaagent.jar", "-jar", "/app/consumer.jar"]
