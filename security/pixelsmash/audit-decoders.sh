#!/usr/bin/env bash
# Аудит: где в системе недоверенный медиаконтент может встретиться с FFmpeg/libavcodec
# без явного действия пользователя (авто-thumbnail, сканирование медиатеки).
#
# Три источника риска (см. pixelsmash-facts.md, п.4 и п.6):
#   1. Desktop-thumbnailer'ы (ffmpegthumbnailer в GNOME/KDE/XFCE) — превью генерируется
#      просто при просмотре папки в файловом менеджере.
#   2. Бинарники, ДИНАМИЧЕСКИ слинкованные с libavcodec — по записи NEEDED в
#      ELF-заголовке (читаем через readelf -d, НЕ через ldd: ldd запускает
#      динамический загрузчик и потому небезопасен на недоверенных бинарниках).
#      ГРАНИЦА МЕТОДА: запись NEEDED есть только у динамической линковки. Если
#      приложение собрано статически или бандлит собственный FFmpeg (как
#      jellyfin-ffmpeg), этот способ его НЕ покажет — такие случаи ловятся
#      разделом 3 (контейнеры) и ручной проверкой вендорских сборок.
#   3. Медиасерверы в docker ps (Jellyfin/Nextcloud/Immich/PhotoPrism/Emby/Kodi),
#      которые сами сканируют библиотеку и генерируют превью новых файлов.
#
# Скрипт только читает состояние системы, ничего не изменяет и не выполняет.

set -euo pipefail

echo "=== Аудит точек авто-срабатывания FFmpeg-декодеров (CVE-2026-8461 / PixelSmash) ==="
echo

# --- 1. Desktop thumbnailer'ы (freedesktop.org thumbnailer spec) ---
echo "-- 1. Thumbnailer'ы файлового менеджера (GNOME/KDE/XFCE) --"

thumbnailer_dirs=(
  "/usr/share/thumbnailers"
  "/usr/local/share/thumbnailers"
)

found_thumbnailer=0
for dir in "${thumbnailer_dirs[@]}"; do
  [ -d "$dir" ] || continue
  # Ищем .thumbnailer файлы, не падаем, если каталог пуст.
  while IFS= read -r -d '' thumb_file; do
    found_thumbnailer=1
    echo "  найден: $thumb_file"
    # Показываем, какая команда реально дергает декодер (обычно Exec=ffmpegthumbnailer ...).
    grep -E '^Exec=' "$thumb_file" 2>/dev/null | sed 's/^/    /' || true
  done < <(find "$dir" -maxdepth 1 -name '*.thumbnailer' -print0 2>/dev/null)
done

if [ "$found_thumbnailer" -eq 0 ]; then
  echo "  файлов *.thumbnailer не найдено в $(printf '%s ' "${thumbnailer_dirs[@]}")"
fi

if command -v ffmpegthumbnailer >/dev/null 2>&1; then
  echo "  бинарник ffmpegthumbnailer установлен: $(command -v ffmpegthumbnailer)"
  echo "  -> файловый менеджер (Nautilus/Dolphin/Thunar) может вызывать его автоматически"
  echo "     при простом просмотре папки с видео — без клика по файлу"
else
  echo "  бинарник ffmpegthumbnailer не найден в PATH"
fi
echo

# --- 2. Бинарники, слинкованные с libavcodec (через readelf, не ldd) ---
echo "-- 2. Бинарники в PATH, ДИНАМИЧЕСКИ слинкованные с libavcodec (запись NEEDED) --"
echo "   (читаем readelf -d, а не ldd — ldd запускает загрузчик, это небезопасно)"
echo "   ГРАНИЦА: статические сборки и приложения с собственным FFmpeg внутри"
echo "   (напр. jellyfin-ffmpeg) записи NEEDED не имеют и здесь НЕ появятся."

if ! command -v readelf >/dev/null 2>&1; then
  echo "  readelf не найден — пропускаем (обычно входит в binutils)"
else
  # Собираем уникальный список каталогов из PATH, чтобы не сканировать дубликаты.
  IFS=':' read -r -a path_dirs <<<"${PATH:-}"
  declare -A seen_dirs=()
  linked_count=0

  for dir in "${path_dirs[@]}"; do
    [ -n "$dir" ] || continue
    [ -d "$dir" ] || continue
    # Пропускаем уже проверенный каталог (в PATH бывают дубли).
    if [ -n "${seen_dirs[$dir]:-}" ]; then
      continue
    fi
    seen_dirs["$dir"]=1

    while IFS= read -r -d '' candidate; do
      [ -f "$candidate" ] || continue
      [ -x "$candidate" ] || continue
      # readelf на не-ELF файле (скрипт, битый файл) просто вернёт ошибку — гасим её.
      if readelf -d "$candidate" 2>/dev/null | grep -q 'libavcodec'; then
        linked_count=$((linked_count + 1))
        echo "  $candidate"
        readelf -d "$candidate" 2>/dev/null | grep 'libavcodec' | sed 's/^/    /'
      fi
    done < <(find "$dir" -maxdepth 1 -type f -print0 2>/dev/null)
  done

  if [ "$linked_count" -eq 0 ]; then
    echo "  бинарников в PATH со ссылкой NEEDED на libavcodec не найдено"
  fi
fi
echo

# --- 3. Медиасерверы среди запущенных Docker-контейнеров ---
echo "-- 3. Медиасерверы в запущенных Docker-контейнерах (авто-сканирование библиотеки) --"

known_media_servers=(jellyfin nextcloud immich photoprism emby kodi)

if ! command -v docker >/dev/null 2>&1; then
  echo "  docker не найден — пропускаем"
elif ! docker info >/dev/null 2>&1; then
  echo "  docker установлен, но демон недоступен — пропускаем"
else
  containers_raw="$(docker ps --format '{{.Names}}\t{{.Image}}' 2>&1 || true)"

  if [ -z "$containers_raw" ]; then
    echo "  запущенных контейнеров нет"
  else
    matched=0
    while IFS=$'\t' read -r name image; do
      # Docker Desktop под Windows отдаёт CRLF — срезаем \r, иначе рвётся вывод.
      name="${name%$'\r'}"
      image="${image%$'\r'}"
      [ -z "$name" ] && continue
      for known in "${known_media_servers[@]}"; do
        # Ищем совпадение и по имени контейнера, и по имени образа (без учёта регистра).
        if [[ "${name,,}" == *"$known"* ]] || [[ "${image,,}" == *"$known"* ]]; then
          matched=1
          echo "  [$known] контейнер '$name' (образ: $image)"
          echo "    -> сам сканирует медиатеку/генерирует превью новых файлов, без ручного открытия видео"
          break
        fi
      done
    done <<<"$containers_raw"

    if [ "$matched" -eq 0 ]; then
      echo "  среди запущенных контейнеров известных медиасерверов не найдено"
    fi
  fi
fi
echo

echo "=== Итог: перечисленные выше точки — места автоматического вызова декодера FFmpeg. ==="
echo "Проверьте версию FFmpeg в каждой (check-ffmpeg-version.sh) и рассмотрите изоляцию"
echo "через sandbox-thumbnailer/ (bwrap-обёртка или hardened docker-compose)."
