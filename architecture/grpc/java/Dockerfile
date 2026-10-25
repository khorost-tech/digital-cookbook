# Сервер и клиент-проба Java в одном образе: `docker run grpc-java server|client …`.
# Контекст сборки — каталог стенда (нужен proto/): docker build -f java/Dockerfile .
FROM maven:3.9-eclipse-temurin-21 AS build
WORKDIR /src/java
COPY java/pom.xml ./
COPY proto /src/proto
# Повторы загрузки: репозитории из Docker Desktop периодически рвут TLS.
RUN for i in 1 2 3 4 5; do mvn -q -B -Daether.connector.http.retryHandler.count=5 dependency:go-offline \
      protobuf:compile protobuf:compile-custom && exit 0; sleep 3; done; exit 1
COPY java/src ./src
RUN mvn -q -B -o package -DskipTests

FROM eclipse-temurin:21-jre-alpine
COPY --from=build /src/java/target/grpc-stand.jar /grpc-stand.jar
COPY java/run.sh /usr/local/bin/
EXPOSE 9090 8081
ENTRYPOINT ["run.sh"]
