# Spark 4.1.3 + Iceberg 1.12.0 (runtime для Spark 4.1 и aws-bundle для S3FileIO).
FROM apache/spark:4.1.3-scala2.13-java17-python3-r-ubuntu@sha256:47442571b32723318fef98198c4d3f27d2804a86e2d4ef5d3eb62337c75abeb6
USER root
ARG ICEBERG=1.12.0
RUN set -e; M=https://repo1.maven.org/maven2/org/apache/iceberg; \
    for a in iceberg-spark-runtime-4.1_2.13 iceberg-aws-bundle; do \
      for i in 1 2 3 4 5; do curl -fsSL -o /opt/spark/jars/$a-$ICEBERG.jar $M/$a/$ICEBERG/$a-$ICEBERG.jar && break; sleep 5; done; \
      test -s /opt/spark/jars/$a-$ICEBERG.jar; done
USER spark
ENV PYTHONPATH=/opt/stand
WORKDIR /opt/stand
CMD ["sleep", "infinity"]
