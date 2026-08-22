# Сборка fat-jar в контейнере (ничего не ставим на хост) и укладка в образ с Flink CLI.
FROM maven:3.9-eclipse-temurin-17 AS build
WORKDIR /app
COPY pom.xml .
RUN mvn -q -B dependency:go-offline
COPY src ./src
RUN mvn -q -B package

FROM flink:1.20.1-java17
COPY --from=build /app/target/flink-datastream.jar /opt/job.jar
