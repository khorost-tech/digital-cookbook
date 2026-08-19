#!/usr/bin/env bash
# Обёртка bwrap (bubblewrap) для запуска декодирования/thumbnailer'а над недоверенным
# медиафайлом в изоляции — на случай CVE-2026-8461 (PixelSmash) и подобных багов в
# libavcodec, до применения фикса (FFmpeg >= 8.1.2 / >= 8.0.3) или в дополнение к нему.
#
# Границы песочницы:
#   - read-only корень хост-системы (--ro-bind / --unshare-all база от bwrap);
#   - --unshare-net       — нет сети: даже при RCE нечего эксфильтровать наружу;
#   - --die-with-parent   — процесс не переживёт родителя (не останется висеть);
#   - --unshare-pid, --unshare-ipc, --unshare-uts — изоляция пространств имён;
#   - --cap-drop ALL      — сброс всех capabilities (действует, если bwrap запущен
#     от root; для непривилегированного пользователя новое user-namespace и так
#     не даёт процессу capabilities на хосте);
#   - приватный /tmp (tmpfs) — не делится с хостом;
#   - /etc монтируется read-only — нужен для резолва библиотек через симлинки
#     update-alternatives (напр. libblas.so.3 -> /etc/alternatives/...), иначе
#     реальный декодер падает с "error while loading shared libraries". Экспозиция
#     безопасна: только чтение + нет сети (нечего эксфильтровать). Хотите жёстче —
#     замените на точечные --ro-bind /etc/alternatives и то, что нужно декодеру;
#   - входной файл монтируется read-only, ТОЛЬКО он (не вся папка с медиатекой);
#   - выходной каталог — единственное место с правом записи;
#   - лимиты ресурсов: ulimit (CPU-время, адресное пространство, размер файла) плюс
#     cgroup через systemd-run (MemoryMax/CPUQuota/TasksMax), если он доступен.
#
# ЧЕГО ЭТО НЕ ДАЁТ: bwrap сам по себе защищает от доступа к данным и эксфильтрации,
# но НЕ от исчерпания ресурсов. Без cgroup-лимитов (см. блок в конце файла) декодер
# может положить хост fork-бомбой или аллокацией памяти. Если systemd-run
# недоступен, скрипт об этом честно предупреждает: остаются только per-process
# ulimit, а число процессов не ограничено ничем.
#
# Использование:
#   ./run.sh <входной-файл> <выходной-каталог> -- <команда> [аргументы...]
#
# Пример:
#   ./run.sh /home/user/videos/suspicious.avi /tmp/thumb-out -- \
#       ffmpegthumbnailer -i /sandbox/in/suspicious.avi -o /sandbox/out/thumb.png -s 256
#
# Примечание: сама команда (ffmpegthumbnailer/ffmpeg) должна быть доступна внутри
# песочницы — см. --ro-bind /usr /usr ниже, который пробрасывает системные бинарники
# и библиотеки хоста в режиме "только чтение".

set -euo pipefail

usage() {
  echo "Использование: $0 <входной-файл> <выходной-каталог> -- <команда> [аргументы...]" >&2
  exit 1
}

if ! command -v bwrap >/dev/null 2>&1; then
  echo "Ошибка: bwrap (bubblewrap) не найден. Установите пакет bubblewrap." >&2
  exit 1
fi

if [ "$#" -lt 4 ]; then
  usage
fi

input_file="$1"
output_dir="$2"
shift 2

if [ "$1" != "--" ]; then
  usage
fi
shift

if [ "$#" -eq 0 ]; then
  usage
fi

if [ ! -f "$input_file" ]; then
  echo "Ошибка: входной файл не найден: $input_file" >&2
  exit 1
fi

mkdir -p "$output_dir"

# Абсолютные пути нужны, т.к. bwrap монтирует их внутрь новой файловой системы.
input_file_abs="$(cd "$(dirname "$input_file")" && pwd)/$(basename "$input_file")"
output_dir_abs="$(cd "$output_dir" && pwd)"

# --- Лимиты ресурсов ---------------------------------------------------------
# ВАЖНО: сам bwrap НЕ ограничивает CPU, память и число процессов — он изолирует
# файлы, сеть и namespace'ы. Без лимитов ниже вредоносный (или просто кривой)
# файл способен положить хост исчерпанием ресурсов: бесконечным циклом, аллокацией
# всей памяти или fork-бомбой. Настраивается переменными окружения.
MEM_MAX="${PIXELSMASH_MEM_MAX:-2G}"            # cgroup MemoryMax
CPU_QUOTA="${PIXELSMASH_CPU_QUOTA:-100%}"      # cgroup CPUQuota (100% = одно ядро)
TASKS_MAX="${PIXELSMASH_TASKS_MAX:-64}"        # cgroup TasksMax (анти-fork-бомба)
CPU_SECONDS="${PIXELSMASH_CPU_SECONDS:-60}"    # ulimit -t, секунды CPU-времени
AS_KB="${PIXELSMASH_AS_KB:-2097152}"           # ulimit -v, адресное пространство (2 GiB)
FSIZE_KB="${PIXELSMASH_FSIZE_KB:-1048576}"     # ulimit -f, размер выходного файла (1 GiB)

# Базовая страховка, работает без systemd: лимиты на ПРОЦЕСС.
# ulimit -u намеренно не трогаем: он ограничивает пользователя целиком, а не
# песочницу, и может задеть посторонние процессы. Число задач ограничивается
# через cgroup TasksMax ниже — если systemd доступен.
ulimit -t "$CPU_SECONDS" 2>/dev/null || true
ulimit -v "$AS_KB" 2>/dev/null || true
ulimit -f "$FSIZE_KB" 2>/dev/null || true

bwrap_cmd=(bwrap
  --die-with-parent
  --unshare-all
  --unshare-net
  --cap-drop ALL
  --ro-bind /usr /usr
  --ro-bind /lib /lib
  --ro-bind-try /lib64 /lib64
  --ro-bind /etc /etc
  --symlink /usr/bin /bin
  --proc /proc
  --dev /dev
  --tmpfs /tmp
  --ro-bind "$input_file_abs" "/sandbox/in/$(basename "$input_file_abs")"
  --bind "$output_dir_abs" /sandbox/out
  --chdir /sandbox
  --new-session
  --clearenv
  --setenv PATH "/usr/bin:/bin"
  "$@")

# Полноценные лимиты (память/CPU/число задач) даёт cgroup через systemd-run.
# Это единственный способ здесь ограничить fork-бомбу.
if command -v systemd-run >/dev/null 2>&1 && systemd-run --user --scope --quiet true 2>/dev/null; then
  exec systemd-run --user --scope --quiet \
    -p "MemoryMax=$MEM_MAX" \
    -p "CPUQuota=$CPU_QUOTA" \
    -p "TasksMax=$TASKS_MAX" \
    "${bwrap_cmd[@]}"
fi

echo "Предупреждение: systemd-run недоступен — cgroup-лимиты (память, CPU, TasksMax) не применены." >&2
echo "  Действуют только per-process ulimit: CPU ${CPU_SECONDS}s, AS ${AS_KB}KB, file ${FSIZE_KB}KB." >&2
echo "  Fork-бомба в этом режиме НЕ ограничена — запускайте под systemd-run/в контейнере с pids_limit." >&2
exec "${bwrap_cmd[@]}"
