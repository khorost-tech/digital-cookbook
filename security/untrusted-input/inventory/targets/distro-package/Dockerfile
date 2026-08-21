# ЦЕЛЬ А — контрольная точка эксперимента.
#
# FFmpeg установлен пакетным менеджером, то есть попал в базу пакетов образа
# (/var/lib/dpkg). Любой сканер образов обязан его увидеть и определить версию.
# Если сканер не находит FFmpeg даже здесь — сломан эксперимент, а не сканер.
FROM debian:12-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg \
 && rm -rf /var/lib/apt/lists/*

CMD ["ffmpeg", "-version"]
