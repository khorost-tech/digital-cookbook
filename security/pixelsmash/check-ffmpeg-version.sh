#!/usr/bin/env bash
# Проверка версии FFmpeg/libavcodec на предмет CVE-2026-8461 (PixelSmash, декодер MagicYUV).
#
# Фикс: FFmpeg 8.1.2 (17.06.2026) на основной ветке; backport в стабильную ветку 8.0.3.
# Логика вердикта:
#   >= 8.1.2                       -> OK (фикс на основной ветке)
#   ветка 8.0.x и версия >= 8.0.3   -> OK (backport-фикс)
#   ниже перечисленного             -> УЯЗВИМО
#   ffmpeg не найден                -> сообщить отдельно, не считать "не уязвимо"
#
# Это ТОЛЬКО диагностика: скрипт ничего не патчит и не изменяет систему.

set -euo pipefail

# Версии-пороги фикса (см. .superpowers/sdd/2026-07-30-pixelsmash-ffmpeg/pixelsmash-facts.md, п.7).
readonly FIX_VERSION_MAIN="8.1.2"
readonly FIX_VERSION_BACKPORT_BRANCH="8.0"
readonly FIX_VERSION_BACKPORT="8.0.3"

# Сравнение двух версий вида X.Y.Z без внешних зависимостей.
# Возвращает 0, если version1 >= version2.
version_ge() {
  local version1="$1"
  local version2="$2"
  [ "$(printf '%s\n%s\n' "$version2" "$version1" | sort -V | head -n1)" = "$version2" ]
}

# Извлекает X.Y.Z (или X.Y) из произвольной строки вида "ffmpeg version 8.1.2-...".
extract_version() {
  local raw="$1"
  grep -oE '[0-9]+\.[0-9]+(\.[0-9]+)?' <<<"$raw" | head -n1 || true
}

# Выполняет shell-строку в нужном контексте: на хосте либо ВНУТРИ контейнера.
# $1 — id контейнера ("" = хост), далее — команда.
run_in_context() {
  local container="$1"
  shift
  if [ -n "$container" ]; then
    docker exec "$container" sh -c "$*" 2>/dev/null
  else
    sh -c "$*" 2>/dev/null
  fi
}

# Возвращает реальный путь бинарника ffmpeg в заданном контексте (с разыменованием
# симлинков). Пусто, если не найден.
resolve_ffmpeg_path() {
  local container="${1:-}"
  # Одинарные кавычки НАМЕРЕННО: строка должна раскрыться в целевом контексте
  # (внутри контейнера), а не здесь — иначе получим путь с хоста.
  # shellcheck disable=SC2016
  run_in_context "$container" \
    'p=$(command -v ffmpeg 2>/dev/null) || exit 0; readlink -f "$p" 2>/dev/null || printf "%s" "$p"' \
    | head -n1
}

# Определяет пакет-ВЛАДЕЛЕЦ конкретного файла. Печатает имя пакета, возвращает 0.
# Возвращает 1, если файл не принадлежит ни одному пакету (кастомная сборка,
# /usr/local/bin, статический бинарник и т.п.).
#
# Зачем: changelog пакета доказывает бэкпорт только для файлов ЭТОГО пакета.
# Если рядом с пропатченным пакетом лежит самосборный /usr/local/bin/ffmpeg,
# то ссылаться на changelog пакета — значит выдать ложный OK уязвимой сборке.
package_owning() {
  local container="${1:-}"
  local path="$2"
  local out=""

  # Debian/Ubuntu: вывод вида "ffmpeg: /usr/bin/ffmpeg".
  out="$(run_in_context "$container" "dpkg -S '$path' 2>/dev/null" | head -n1)"
  if [ -n "$out" ]; then
    printf '%s' "${out%%:*}"
    return 0
  fi

  # RHEL/Fedora: имя пакета либо сообщение "... is not owned by any package".
  out="$(run_in_context "$container" "rpm -qf '$path' 2>/dev/null" | head -n1)"
  case "$out" in
    ""|*"not owned"*|*"не принадлежит"*|*"no package"*) return 1 ;;
    *) printf '%s' "$out"; return 0 ;;
  esac
}

# Ищет упоминание CVE в changelog КОНКРЕТНОГО пакета — строго в том же контексте,
# где найден ffmpeg. Метаданные хоста ничего не говорят о содержимом контейнера:
# пропатченный пакет на хосте и уязвимый FFmpeg в образе — обычная ситуация.
# $1 — id контейнера ("" = хост); $2 — имя пакета-владельца (обязательно).
# Возвращает 0, если найдено свидетельство бэкпорта.
distro_backport_found() {
  local container="${1:-}"
  local pkg="$2"
  local cve="CVE-2026-8461"
  local where="хост"
  [ -n "$container" ] && where="контейнер"
  [ -n "$pkg" ] || return 1

  # RHEL/Fedora/CentOS: rpm хранит changelog с перечнем бэкпортнутых CVE.
  if run_in_context "$container" "rpm -q --changelog '$pkg'" | grep -qi "$cve"; then
    echo "    changelog rpm ($pkg, $where) упоминает $cve — фикс бэкпортнут"
    return 0
  fi

  # Debian/Ubuntu: changelog пакета лежит в /usr/share/doc/<пакет>/.
  if run_in_context "$container" \
      "zcat /usr/share/doc/'$pkg'/changelog.Debian.gz 2>/dev/null" | grep -qi "$cve"; then
    echo "    changelog.Debian ($pkg, $where) упоминает $cve — фикс бэкпортнут"
    return 0
  fi

  # apt-get changelog ходит в сеть, поэтому только для хоста и только с таймаутом:
  # внутри контейнера это почти всегда бесполезно и рискует подвесить аудит.
  if [ -z "$container" ] && command -v apt-get >/dev/null 2>&1; then
    local apt_changelog=""
    if command -v timeout >/dev/null 2>&1; then
      apt_changelog="$(timeout 15 apt-get changelog "$pkg" 2>/dev/null || true)"
    else
      apt_changelog="$(apt-get changelog "$pkg" 2>/dev/null || true)"
    fi
    if printf '%s' "$apt_changelog" | grep -qi "$cve"; then
      echo "    apt changelog ($pkg, хост) упоминает $cve — фикс бэкпортнут"
      return 0
    fi
  fi

  return 1
}

# Печатает вердикт по версии.
#
# ВАЖНО: номер версии решает вопрос только для сборок из АПСТРИМА. Дистрибутивы
# (Debian, Ubuntu, RHEL и др.) обычно бэкпортят патч в свою версию, НЕ поднимая
# upstream-номер: в NVD, например, значится исправленный пакет Red Hat на базе
# FFmpeg 6.1.6. Поэтому «версия ниже порога» != «уязвимо»: это повод проверить
# changelog пакета и бюллетень вендора, а не готовый вердикт.
# $3 — id контейнера ("" или отсутствует = хост). Контекст обязателен: поиск
# бэкпорта должен идти там же, где найден сам ffmpeg.
judge_version() {
  local version="$1"
  local label="$2"
  local container="${3:-}"

  if [ -z "$version" ]; then
    echo "  [$label] версия не распознана — проверьте вручную"
    return
  fi

  local major_minor="${version%.*}"

  if version_ge "$version" "$FIX_VERSION_MAIN"; then
    echo "  [$label] $version -- OK (>= $FIX_VERSION_MAIN, фикс на основной ветке)"
    return
  fi

  if [ "$major_minor" = "$FIX_VERSION_BACKPORT_BRANCH" ] && version_ge "$version" "$FIX_VERSION_BACKPORT"; then
    echo "  [$label] $version -- OK (ветка 8.0.x, backport-фикс >= $FIX_VERSION_BACKPORT)"
    return
  fi

  # Ниже апстрим-порога. Прежде чем верить changelog, надо доказать, что найденный
  # бинарник ВООБЩЕ принадлежит пакету дистрибутива: самосборный /usr/local/bin/ffmpeg
  # рядом с пропатченным пакетом иначе получил бы чужой вердикт OK.
  local binpath owner=""
  binpath="$(resolve_ffmpeg_path "$container")"

  if [ -n "$binpath" ]; then
    owner="$(package_owning "$container" "$binpath" || true)"
  fi

  if [ -n "$owner" ]; then
    echo "    бинарник: $binpath (пакет: $owner)"
    # Ищем следы бэкпорта ТАМ ЖЕ, где найден ffmpeg, и именно в пакете-владельце.
    if distro_backport_found "$container" "$owner"; then
      echo "  [$label] $version -- OK (upstream-версия ниже $FIX_VERSION_MAIN, но фикс бэкпортнут в пакете $owner)"
      return
    fi
  fi

  echo "  [$label] $version -- ТРЕБУЕТ ПРОВЕРКИ (ниже апстрим-порога $FIX_VERSION_MAIN)"

  if [ -z "$owner" ]; then
    # Непакетная сборка: механизм changelog к ней неприменим в принципе.
    if [ -n "$binpath" ]; then
      echo "    бинарник: $binpath — НЕ принадлежит ни одному пакету дистрибутива"
    else
      echo "    путь бинарника определить не удалось"
    fi
    echo "    Это самосборный/вендорский FFmpeg: changelog пакета к нему неприменим,"
    echo "    и бэкпорт по нему доказать нельзя. Проверяйте, из какого коммита он"
    echo "    собран (см. ffmpeg -version, строку configuration) и есть ли в нём"
    echo "    фикс pr/23159; либо пересоберите на 8.1.2+ / с --disable-decoder=magicyuv."
    return
  fi

  echo "    Если это сборка из апстрима — она уязвима (CVE-2026-8461)."
  echo "    Если пакет дистрибутива — фикс могли бэкпортнуть без смены версии;"
  echo "    следов в changelog пакета $owner не найдено, но не все вендоры их пишут."
  echo "    Сверьтесь с security-бюллетенем дистрибутива."
}

echo "=== Проверка CVE-2026-8461 (PixelSmash, FFmpeg/MagicYUV decoder) ==="
echo

# --- 1. ffmpeg в хост-системе ---
echo "-- Хост-система --"
if command -v ffmpeg >/dev/null 2>&1; then
  ffmpeg_raw="$(ffmpeg -version 2>&1 | head -n1)"
  ffmpeg_version="$(extract_version "$ffmpeg_raw")"
  echo "  Найден: $ffmpeg_raw"
  judge_version "$ffmpeg_version" "хост ffmpeg"
else
  echo "  ffmpeg не найден в PATH хост-системы"
fi
echo

# --- 2. Запущенные Docker-контейнеры ---
echo "-- Запущенные Docker-контейнеры --"
if ! command -v docker >/dev/null 2>&1; then
  echo "  docker не найден — пропускаем проверку контейнеров"
  exit 0
fi

if ! docker info >/dev/null 2>&1; then
  echo "  docker установлен, но демон недоступен — пропускаем проверку контейнеров"
  exit 0
fi

container_ids="$(docker ps --format '{{.ID}}' 2>&1 || true)"

if [ -z "$container_ids" ]; then
  echo "  запущенных контейнеров нет"
  exit 0
fi

while IFS= read -r container_id; do
  # Docker Desktop под Windows отдаёт CRLF — иначе \r попадёт в ID и порвёт вывод.
  container_id="${container_id%$'\r'}"
  [ -z "$container_id" ] && continue
  container_name="$(docker inspect --format '{{.Name}}' "$container_id" 2>/dev/null || echo "$container_id")"
  container_name="${container_name%$'\r'}"

  # exec может не сработать (нет ffmpeg в контейнере, нет /bin/sh и т.д.) — это не ошибка проверки.
  # stderr гасим ВНУТРИ sh: иначе "sh: ffmpeg: not found" попадёт в stdout и будет
  # принято за строку версии.
  container_ffmpeg_raw="$(docker exec "$container_id" sh -c 'ffmpeg -version 2>/dev/null | head -n1' 2>/dev/null || true)"

  # Подстраховка: считаем находкой только то, что действительно похоже на версию.
  case "$container_ffmpeg_raw" in
    ffmpeg\ version*) ;;
    *) container_ffmpeg_raw="" ;;
  esac

  if [ -z "$container_ffmpeg_raw" ]; then
    echo "  [$container_name] ffmpeg не найден внутри контейнера (или exec недоступен)"
    continue
  fi

  container_ffmpeg_version="$(extract_version "$container_ffmpeg_raw")"
  echo "  [$container_name] найден: $container_ffmpeg_raw"
  # Третий аргумент обязателен: бэкпорт ищем ВНУТРИ этого контейнера, а не на хосте.
  judge_version "$container_ffmpeg_version" "$container_name" "$container_id"
done <<<"$container_ids"
