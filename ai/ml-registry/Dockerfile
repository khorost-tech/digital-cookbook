# MLflow 3.16 (сервер, клиент, mlflow models serve), DVC 3.67 и окружения со старыми scikit-learn — по requirements*.txt с хешами.
FROM python:3.12-slim@sha256:a6e34c598f2467ed0e9a8d349809fcd8b5c603269512df273a0bb1784edc11b1
COPY --from=ghcr.io/astral-sh/uv:0.12.5@sha256:e85be844203885286c60ffad8a858d48afb6c5a5c237ca0e67f12e74b8f174b1 /uv /usr/local/bin/uv
RUN apt-get update && apt-get install -y --no-install-recommends git && rm -rf /var/lib/apt/lists/*
COPY requirements.txt /tmp/requirements.txt
COPY envs/ /tmp/envs/
RUN for i in 1 2 3; do uv pip install --system --require-hashes -r /tmp/requirements.txt && break; sleep 5; done
# старые scikit-learn — отдельные окружения: в каждом только он, numpy той же версии, что в основном, и зависимости
RUN for v in 1.8.0 1.7.2 1.6.1; do uv venv /opt/sklearn-$v && \
      uv pip install --python /opt/sklearn-$v/bin/python --require-hashes -r /tmp/envs/sklearn-$v.txt; done
ENV OMP_NUM_THREADS=1 PYTHONDONTWRITEBYTECODE=1 PYTHONHASHSEED=0 GIT_AUTHOR_NAME=stand GIT_AUTHOR_EMAIL=stand@localhost \
    GIT_COMMITTER_NAME=stand GIT_COMMITTER_EMAIL=stand@localhost
WORKDIR /work
