FROM maven:3.9-eclipse-temurin-21 AS build
WORKDIR /src
COPY pom.xml ./
# Повторы загрузки: репозитории из Docker Desktop периодически рвут TLS.
RUN for i in 1 2 3 4 5; do mvn -q -B -Daether.connector.http.retryHandler.count=5 dependency:go-offline && exit 0; sleep 3; done; exit 1
COPY src ./src
RUN mvn -q -B -o package -DskipTests

FROM eclipse-temurin:21-jre-alpine
COPY --from=build /src/target/server.jar /server.jar
EXPOSE 8080
# Куча фиксирована: память на соединение (ось 6) не должна зависеть от того,
# сколько JVM решила зарезервировать сама.
ENV JAVA_TOOL_OPTIONS="-Xms256m -Xmx256m -XX:+UseG1GC"
ENTRYPOINT ["java", "-jar", "/server.jar"]
