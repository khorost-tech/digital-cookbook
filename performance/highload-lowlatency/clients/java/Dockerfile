# Multi-stage: JDK 21 (Maven) для сборки runnable jar -> JRE 21 для рантайма.
FROM maven:3.9-eclipse-temurin-21 AS build
WORKDIR /src
COPY pom.xml ./
# Прогреваем локальный кэш зависимостей отдельным слоем (зависимостей нет,
# но команда стабильно резолвит plugins/parent POM в кэш до COPY src).
RUN mvn -q -B dependency:go-offline || true
COPY src ./src
RUN mvn -q -B -DskipTests package

FROM eclipse-temurin:21-jre-alpine
WORKDIR /app
COPY --from=build /src/target/highload-java-client.jar /app/app.jar
# Дефолтный путь payload — /payload/sample-request.json (см. PAYLOAD env var).
# В образ он не встраивается: build-context ограничен этой директорией,
# смонтируйте topology/payload/sample-request.json стенда volume'ом на этот
# путь, либо укажите PAYLOAD на другой файл. Без смонтированного файла
# клиент использует встроенный fallback-payload и всё равно запускается.
ENTRYPOINT ["java", "-jar", "/app/app.jar"]
