# Образ Picodata собран на AlmaLinux 8.10 (glibc 2.28 — проверено `ldd --version`
# внутри образа). Плагин — динамическая библиотека, которую грузит именно этот
# процесс, поэтому собирать её нужно против того же glibc. Сборка в WSL/Ubuntu
# (glibc 2.39) даёт .so с требованием более новой версии, и включение плагина
# падает на загрузке библиотеки.
FROM almalinux:8.10

RUN dnf install -y gcc make git openssl-devel pkgconfig cmake && dnf clean all
# rustfmt и clippy ставятся явно: минимальный профиль rustup их не включает, а
# опубликованный проект должен проходить штатный форматтер и линтер.
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs \
    | sh -s -- -y --default-toolchain stable --profile minimal \
      --component rustfmt --component clippy
ENV PATH="/root/.cargo/bin:${PATH}"
RUN cargo install picodata-pike --version 5.4.0 --locked

WORKDIR /src
