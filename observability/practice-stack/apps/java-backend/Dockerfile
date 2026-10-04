# Сборка и запуск на JDK 21: Spring Boot 4.1 требует минимум 17, берём LTS 21.
FROM maven:3.9-eclipse-temurin-21 AS build
WORKDIR /src
# Слой зависимостей отдельно от кода: правка Java-файла не тянет повторную
# загрузку всего Maven-репозитория.
COPY pom.xml ./
RUN mvn -q -B dependency:go-offline
COPY src ./src
RUN mvn -q -B package -DskipTests

FROM eclipse-temurin:21-jre
WORKDIR /app
# Агент OTel скачивается на этапе сборки образа, а не кладётся в репозиторий:
# 24 МБ бинарника в git — плохая идея. Версия пинуется.
ARG OTEL_AGENT_VERSION=2.30.0
ADD https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/download/v${OTEL_AGENT_VERSION}/opentelemetry-javaagent.jar /app/opentelemetry-javaagent.jar
# Агент Pyroscope — второй javaagent. Два агента в одной JVM сосуществуют
# нормально: OTel занимается трейсами и метриками, Pyroscope — профилями, и
# точки инструментирования у них разные.
ARG PYROSCOPE_AGENT_VERSION=2.9.0
ADD https://repo1.maven.org/maven2/io/pyroscope/agent/${PYROSCOPE_AGENT_VERSION}/agent-${PYROSCOPE_AGENT_VERSION}.jar /app/pyroscope.jar
COPY --from=build /src/target/java-backend-1.0.0.jar /app/app.jar
RUN chmod 0444 /app/opentelemetry-javaagent.jar /app/pyroscope.jar && useradd -u 10001 -m app
USER app
EXPOSE 8080
# Агент НЕ включается здесь: включение делается переменной JAVA_TOOL_OPTIONS в
# compose. Так один и тот же образ даёт и базовую линию без телеметрии, и
# вариант с агентом — иначе замер накладных расходов сравнивал бы разные образы.
ENTRYPOINT ["java", "-jar", "/app/app.jar"]
