FROM flink:1.20.1-java17

# Тот же Kafka SQL-коннектор, что и в 01-state.
ARG KAFKA_CONNECTOR=3.3.0-1.20
RUN wget -q -P /opt/flink/lib \
    https://repo1.maven.org/maven2/org/apache/flink/flink-sql-connector-kafka/${KAFKA_CONNECTOR}/flink-sql-connector-kafka-${KAFKA_CONNECTOR}.jar

# Каталоги снимков создаём в образе и отдаём пользователю flink (uid 9999), под которым работает
# JVM. Иначе именованные тома монтируются в несуществующие пути, Docker создаёт точки монтирования
# как root:root 0755, и джоба падает на старте с "Failed to create directory for shared state".
RUN mkdir -p /tmp/flink-checkpoints /tmp/flink-savepoints     && chown flink:flink /tmp/flink-checkpoints /tmp/flink-savepoints
