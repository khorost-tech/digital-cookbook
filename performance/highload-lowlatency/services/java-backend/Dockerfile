# Multi-stage: JDK 21 (Maven) для сборки shaded jar -> JRE 21 для рантайма.
FROM maven:3.9-eclipse-temurin-21 AS build
WORKDIR /src
COPY pom.xml ./
# Прогреваем локальный кэш зависимостей отдельным слоем.
RUN mvn -q -B dependency:go-offline || true
COPY src ./src
RUN mvn -q -B -DskipTests package

FROM eclipse-temurin:21-jre-alpine
WORKDIR /app
COPY --from=build /src/target/highload-java-backend.jar /app/app.jar
EXPOSE 9000
ENTRYPOINT ["java", "-jar", "/app/app.jar"]
