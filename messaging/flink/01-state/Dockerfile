FROM flink:1.20.1-java17

# Flink SQL Kafka connector (uber-jar) в /opt/flink/lib, чтобы SQL client видел коннектор 'kafka'.
ARG KAFKA_CONNECTOR=3.3.0-1.20
RUN wget -q -P /opt/flink/lib \
    https://repo1.maven.org/maven2/org/apache/flink/flink-sql-connector-kafka/${KAFKA_CONNECTOR}/flink-sql-connector-kafka-${KAFKA_CONNECTOR}.jar

# Каталог чекпоинтов создаём в образе и отдаём пользователю flink (uid 9999), под которым
# работает JVM. Иначе именованный том монтируется в несуществующий путь, Docker создаёт
# точку монтирования как root:root 0755, и джоба падает на старте:
# "Failed to create directory for shared state: file:/tmp/flink-checkpoints/<job>/shared".
RUN mkdir -p /tmp/flink-checkpoints && chown flink:flink /tmp/flink-checkpoints
