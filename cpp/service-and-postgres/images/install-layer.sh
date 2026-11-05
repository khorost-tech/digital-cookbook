#!/bin/sh
# Установка ТОЛЬКО библиотеки слоя HTTP — больше в образе слоя не
# появляется ничего. Компилятор, система сборки и клиент базы стоят в
# базовом образе и у трёх вариантов одни и те же.
#
# ПОЧЕМУ ЭТО ОДИН СЦЕНАРИЙ, А НЕ ТРИ СТРОКИ В ТРЁХ DOCKERFILE'АХ. Три
# файла слоёв обязаны быть построчно симметричны: одинаковые шаги,
# одинаковый порядок, различается ровно слой. Вторая переменная (другой
# флаг apt, другой порядок шагов, забытая чистка кеша) приехала бы
# незаметно, и расхождение замера объяснялось бы двумя причинами сразу.
# Здесь различие сведено к ОДНОЙ величине — имени слоя.
set -eu

layer="${1:-}"

case "$layer" in
    beast)
        # Boost.Beast — заголовочная библиотека внутри Boost.
        packages="libboost-dev"
        ;;
    drogon)
        # Список длиннее одного пакета не по недосмотру. DrogonConfig.cmake
        # объявляет зависимостями ВСЁ, с чем Drogon собран в Debian —
        # jsoncpp, uuid, ssl, zlib, brotli и клиентов трёх баз, — и требует
        # их у того, кто с ним линкуется; рекомендованные пакеты здесь не
        # ставятся. Это зависимости слоя, и место им ровно тут: в базовый
        # образ они не идут, иначе два других варианта получили бы то,
        # чего их слой не требует.
        packages="libdrogon-dev libjsoncpp-dev uuid-dev libssl-dev
                  libz-dev libbrotli-dev libsqlite3-dev libpq-dev
                  libmariadb-dev libmariadb-dev-compat libhiredis-dev
                  libyaml-cpp-dev"
        ;;
    crow)
        # Crow в дистрибутиве не упакован; из репозитория ставится только
        # asio, на котором он работает. Сам слой приезжает ниже одним
        # заголовком, закреплённым суммой.
        packages="libasio-dev"
        ;;
    *)
        echo "неизвестный слой HTTP: '$layer'" >&2
        exit 2
        ;;
esac

apt-get update -qq
# shellcheck disable=SC2086  # список пакетов обязан разбиться на слова
apt-get install -y -qq --no-install-recommends $packages
rm -rf /var/lib/apt/lists/*

if [ "$layer" = "crow" ]; then
    # Версия и сумма закреплены здесь: без суммы «тот же» заголовок
    # завтра приехал бы другим, и фикстуры сравнивались бы с чужим
    # стендом молча. Сверка стоит ДО установки заголовка — иначе она
    # ничего не решает.
    crow_version="v1.2.1.2"
    crow_sha256="1f22dd63a65685ed9e22803bdc675428ca1c81e51d2bc6912db6beb1b357f582"
    curl -fsSL -o /tmp/crow_all.h \
        "https://github.com/CrowCpp/Crow/releases/download/${crow_version}/crow_all.h"
    echo "${crow_sha256}  /tmp/crow_all.h" | sha256sum -c -
    install -m 0644 /tmp/crow_all.h /usr/local/include/crow_all.h
    rm -f /tmp/crow_all.h
fi
