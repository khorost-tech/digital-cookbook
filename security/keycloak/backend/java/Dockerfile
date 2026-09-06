# Multi-stage build для Spring Boot resource server (зеркало Go-сервиса).
# Сборка на JDK 21 + Maven, рантайм на JRE 21, non-root.
FROM maven:3.9-eclipse-temurin-21 AS build
WORKDIR /src
# Сначала pom.xml — слой зависимостей кэшируется отдельно от исходников.
COPY pom.xml ./
RUN mvn -q -B -e dependency:go-offline
COPY src ./src
# ГОНЯЕМ тесты при сборке образа (mvn package запускает surefire). На JDK 21 Mockito
# self-attach разрешён через surefire argLine -XX:+EnableDynamicAgentLoading (см. pom.xml).
RUN mvn -q -B -e package \
    && cp target/keycloak-demo-*.jar /app.jar

# Минимальный рантайм, non-root.
FROM eclipse-temurin:21-jre
RUN groupadd -r app && useradd -r -g app -u 10001 app
WORKDIR /app
COPY --from=build /app.jar /app/app.jar
USER app
EXPOSE 8082
ENTRYPOINT ["java", "-jar", "/app/app.jar"]
