# PyIceberg (протокол коммита), pyarrow («паркет в папке»), boto3 (объекты S3).
FROM python:3.12-slim@sha256:05cda9777409a9c3ffddd94a4c476b79f0769a0b4857f0c7ed9226b6800b0d6f
RUN for i in 1 2 3; do pip install --no-cache-dir "pyiceberg[pyarrow]==0.12.0" pyarrow==25.0.1 boto3==1.43.109 && break; sleep 5; done
ENV PYTHONPATH=/opt/stand
WORKDIR /opt/stand
CMD ["sleep", "infinity"]
