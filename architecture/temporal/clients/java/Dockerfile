# Сборка внутри образа: Maven на хосте отсутствует, а собирать на drvfs
# нельзя — запись артефактов срывается.
FROM maven:3.9-eclipse-temurin-21 AS build
WORKDIR /app
COPY pom.xml .
RUN mvn -B -q dependency:go-offline
COPY src ./src
RUN mvn -B -q package -DskipTests

FROM eclipse-temurin:21-jre
WORKDIR /app
COPY --from=build /app/target/worker.jar app.jar
ENTRYPOINT ["java", "-jar", "app.jar"]
