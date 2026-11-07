# python:3.12-slim — по дайджесту из images.json
FROM python:3.12-slim@sha256:a6e34c598f2467ed0e9a8d349809fcd8b5c603269512df273a0bb1784edc11b1
RUN pip install --no-cache-dir faiss-cpu==1.10.0 numpy==2.2.3
COPY check.py /check.py
ENTRYPOINT ["python", "/check.py"]
