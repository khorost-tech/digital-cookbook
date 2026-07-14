# Собираем и запускаем через maven — для демо не выделяем отдельный runtime-образ.
FROM maven:3.9-eclipse-temurin-21
WORKDIR /src
COPY pom.xml ./
# Прогреваем локальный кэш зависимостей отдельным слоем.
RUN mvn -q -B dependency:resolve || true
COPY src ./src
CMD ["mvn", "-q", "-B", "compile", "exec:java"]
