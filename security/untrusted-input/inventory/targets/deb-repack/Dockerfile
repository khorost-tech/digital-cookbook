# КОНТРОЛЬНЫЙ ЭКСПЕРИМЕНТ: один и тот же бинарник, два разных имени пакета.
#
# Цели distro/vendored/jellyfin показательны, но в них различаются и версии, и
# дистрибутивы, поэтому по ним нельзя отделить влияние ИМЕНИ пакета от влияния
# всего остального. Здесь зафиксировано всё: один и тот же файл ffmpeg
# (побайтово — проверяется sha256 при сборке стенда), одна база, одна версия.
# Меняется ТОЛЬКО имя пакета в базе dpkg.
#
# Собирается дважды:
#   docker build --build-arg PKGNAME=ffmpeg           -t ui-inv-deb-canonical:local .
#   docker build --build-arg PKGNAME=acmecorp-ffmpeg7 -t ui-inv-deb-renamed:local  .
#
# Донор пиновался по digest, а не по тегу: тег изменяем, и без digest
# утверждения о содержимом невоспроизводимы.
# jrottenberg/ffmpeg:8.1-ubuntu2404, digest получен 2026-07-31.
#
# Почему ubuntu-донор, а не alpine: бинарник должен реально запускаться в целевом
# образе. Alpine-сборка собрана под musl, и в glibc-образе она не только не
# работает — подсунутые через LD_LIBRARY_PATH musl-библиотеки роняют segfault'ом
# сам dpkg. База ubuntu:24.04 совпадает с базой донора, поэтому glibc сходится.
FROM jrottenberg/ffmpeg@sha256:83ef82d9850314baa3504821e2ea6598e40e2096ac8f967a842d31234be2be92 AS vendor

# База пиновалась по digest: без него «одна и та же база» — обещание, а не факт.
# ubuntu:24.04, digest получен 2026-07-31.
FROM ubuntu@sha256:4fbb8e6a8395de5a7550b33509421a2bafbc0aab6c06ba2cef9ebffbc7092d90 AS builder
ARG PKGNAME=ffmpeg
# Версия намеренно совпадает с тем, что бинарник сообщает о себе сам.
ARG PKGVERSION=8.1

RUN mkdir -p /pkg/DEBIAN /pkg/usr/bin /pkg/usr/lib/vendored
COPY --from=vendor /usr/local/bin/ffmpeg /pkg/usr/bin/ffmpeg
COPY --from=vendor /usr/local/lib/       /pkg/usr/lib/vendored/

# Минимальная контрольная запись пакета: именно она попадает в /var/lib/dpkg
# и именно её читают сканеры образов.
RUN printf 'Package: %s\nVersion: %s\nArchitecture: amd64\nMaintainer: stand <stand@example.invalid>\nDescription: repacked ffmpeg binary for component-identity experiment\n' \
      "$PKGNAME" "$PKGVERSION" > /pkg/DEBIAN/control \
 && dpkg-deb --build /pkg /tmp/pkg.deb

FROM ubuntu@sha256:4fbb8e6a8395de5a7550b33509421a2bafbc0aab6c06ba2cef9ebffbc7092d90
COPY --from=builder /tmp/pkg.deb /tmp/pkg.deb
RUN dpkg -i /tmp/pkg.deb && rm -f /tmp/pkg.deb

# Библиотеки прописываем через ld.so.conf, а не LD_LIBRARY_PATH: переменная
# окружения действует на ВСЕ процессы в образе, включая системные утилиты.
RUN echo /usr/lib/vendored > /etc/ld.so.conf.d/vendored.conf && ldconfig

CMD ["/usr/bin/ffmpeg", "-version"]
