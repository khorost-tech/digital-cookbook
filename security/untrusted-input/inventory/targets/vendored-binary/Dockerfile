# ЦЕЛЬ Б — слепое пятно в чистом виде.
#
# Тот же FFmpeg, что и в цели А, но положенный в образ МИМО пакетного менеджера —
# ровно так живут вендорские сборки (jellyfin-ffmpeg и подобные): бинарник и
# библиотеки просто копируются в файловую систему образа.
#
# База намеренно совпадает с базой донора (Alpine 3.20), поэтому бинарник здесь
# не декоративный — он реально запускается. При этом в базе пакетов apk записи о
# FFmpeg нет: `apk info` его не знает. Именно оттуда сканеры образов берут
# список компонентов — и в этом вся суть эксперимента.
# Донор пиновался по digest, а не по тегу: тег изменяем, и без digest любые
# утверждения о содержимом невоспроизводимы.
# jrottenberg/ffmpeg:8.1.2-alpine320, digest получен 2026-07-31.
FROM jrottenberg/ffmpeg@sha256:1e7192a349eb5fdf9dd7166d862af473b4ee0bac5f68a933c07622ce4015f579 AS vendor

FROM alpine:3.20

# Бинарники и разделяемые библиотеки кладём в отдельный префикс,
# чтобы не смешивать с системными путями дистрибутива.
COPY --from=vendor /bin/ffmpeg   /opt/vendored/bin/ffmpeg
COPY --from=vendor /bin/ffprobe  /opt/vendored/bin/ffprobe
COPY --from=vendor /lib/         /opt/vendored/lib/

ENV PATH="/opt/vendored/bin:${PATH}"
ENV LD_LIBRARY_PATH="/opt/vendored/lib"

CMD ["/opt/vendored/bin/ffmpeg", "-version"]
