# Генератор датасета и базовая модель: numpy и scikit-learn по requirements.txt с хешами.
FROM python:3.12-slim@sha256:a6e34c598f2467ed0e9a8d349809fcd8b5c603269512df273a0bb1784edc11b1
COPY --from=ghcr.io/astral-sh/uv:0.12.5@sha256:e85be844203885286c60ffad8a858d48afb6c5a5c237ca0e67f12e74b8f174b1 /uv /usr/local/bin/uv
COPY requirements.txt /tmp/requirements.txt
RUN for i in 1 2 3; do uv pip install --system --require-hashes -r /tmp/requirements.txt && break; sleep 5; done
ENV OMP_NUM_THREADS=1 PYTHONDONTWRITEBYTECODE=1 PYTHONHASHSEED=0
WORKDIR /work
