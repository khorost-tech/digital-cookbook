# Собирать из корня модуля security/auth:
#   docker build -f webauthn/backend-java/Dockerfile .
FROM maven:3.9-eclipse-temurin-21 AS builder
WORKDIR /src

COPY webauthn/backend-java/pom.xml ./pom.xml
RUN mvn -q -B -DskipTests dependency:go-offline

COPY webauthn/backend-java/src ./src
# Frontend (Task 6: web/index.html+app.js) — тот же файл, что раздаёт
# webauthn-go, копируется в classpath:/static/, откуда Spring Boot отдаёт его
# по умолчанию (index.html на "/", остальное как статику) с того же origin,
# что и /register|login/*.
COPY webauthn/web ./src/main/resources/static
RUN mvn -q -B -DskipTests package

FROM eclipse-temurin:21-jre
RUN useradd --system --create-home --shell /usr/sbin/nologin webauthn
USER webauthn
COPY --from=builder /src/target/webauthn-backend-java.jar /app/webauthn-backend-java.jar
EXPOSE 8088
ENTRYPOINT ["java", "-jar", "/app/webauthn-backend-java.jar"]
