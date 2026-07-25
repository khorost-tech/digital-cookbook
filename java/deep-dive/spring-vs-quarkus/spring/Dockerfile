# Spring Boot 3.5.3 JVM-режим: обычный fat-jar (spring-boot-maven-plugin
# repackage), рантайм — стоковый temurin:25-jre. Аналог режима "fat" из
# ../build-packaging.
FROM eclipse-temurin:25-jre
WORKDIR /app
COPY target/app.jar /app/app.jar
ENV MODE=spring-boot-jvm
EXPOSE 8080
CMD ["java", "-jar", "/app/app.jar"]
