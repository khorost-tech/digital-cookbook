# Java-клиент стенда: neo4j-java-driver (bolt) + JDBC (AGE + baseline).
# Multi-stage: сборка uber-jar в maven-образе, запуск в JRE.
FROM maven:3.9-eclipse-temurin-21 AS build
WORKDIR /src
COPY pom.xml .
RUN mvn -q -B dependency:go-offline -DskipTests || true
COPY src ./src
RUN mvn -q -B -DskipTests package

FROM eclipse-temurin:21-jre
COPY --from=build /src/target/graph-client.jar /graph-client.jar
ENTRYPOINT ["java", "-jar", "/graph-client.jar"]
