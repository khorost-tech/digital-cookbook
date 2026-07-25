# Quarkus 3.22.3 JVM-режим: дефолтная fast-jar раскладка Maven-плагина
# (target/quarkus-app/{quarkus-run.jar,lib,app,quarkus}), рантайм -- тот же
# стоковый temurin:25-jre, что у Spring-стороны (../spring/Dockerfile) --
# сравнение честное, один и тот же базовый образ JRE.
FROM eclipse-temurin:25-jre
WORKDIR /app
COPY target/quarkus-app/ /app/
ENV MODE=quarkus-jvm
EXPOSE 8080
CMD ["java", "-jar", "/app/quarkus-run.jar"]
