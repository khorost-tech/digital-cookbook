# python:3.12-slim — по дайджесту из images.json
FROM python:3.12-slim@sha256:a6e34c598f2467ed0e9a8d349809fcd8b5c603269512df273a0bb1784edc11b1
RUN pip install --no-cache-dir torch==2.6.0 --index-url https://download.pytorch.org/whl/cpu \
 && pip install --no-cache-dir sentence-transformers==3.4.1 transformers==4.49.0 numpy==2.2.3
ARG MODEL=intfloat/multilingual-e5-small
ARG REVISION=614241f622f53c4eeff9890bdc4f31cfecc418b3
ENV MODEL=$MODEL REVISION=$REVISION
RUN python -c "from sentence_transformers import SentenceTransformer as S; S('$MODEL', revision='$REVISION')"
COPY embed.py /embed.py
WORKDIR /data
ENTRYPOINT ["python", "/embed.py"]
