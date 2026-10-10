#!/usr/bin/env bash
# capture.sh — снять живой вывод в fixtures/<name>.txt, записав перед ним точную команду.
#
#   capture.sh [-a] [-t заголовок] <name> -- <kubectl args…>   # команда kubectl
#   capture.sh [-a] [-t заголовок] <name> --sh '<конвейер>'    # конвейер/цикл через bash -c
#
#   -a  дописать секцию в конец файла (без -a файл перезаписывается с шапкой)
#   -t  заголовок секции: строка «--- заголовок ---» перед командой
#
# Каждая секция начинается с «# cmd: …» — команды в виде, который можно скопировать
# в терминал как есть (аргументы с пробелами, {}, кавычками и т. п. взяты в кавычки).
# Статья берёт команду для листинга именно отсюда.
set -euo pipefail
: "${KUBECONFIG:?set KUBECONFIG to k8s-volga kubeconfig}"
ctx="$(kubectl config current-context)"
[ "$ctx" = "admin@k8s-volga" ] || { echo "WRONG CONTEXT: $ctx (ожидался admin@k8s-volga)"; exit 1; }

append=0; title=""
while getopts "at:" opt; do
  case "$opt" in
    a) append=1 ;;
    t) title="$OPTARG" ;;
    *) echo "usage: capture.sh [-a] [-t title] <name> -- <kubectl args…> | --sh '<pipeline>'" >&2; exit 2 ;;
  esac
done
shift $((OPTIND - 1))
name="${1:?fixture name required}"; shift

# Аргумент в shell-синтаксисе: безопасные — как есть, остальные — в одинарных кавычках.
quote() {
  local a out="" sq="'\\''"
  for a in "$@"; do
    if [[ "$a" =~ ^[A-Za-z0-9_./:=,@%+-]+$ ]]; then
      out+=" $a"
    else
      out+=" '${a//\'/$sq}'"
    fi
  done
  printf '%s' "${out# }"
}

case "${1:-}" in
  --) shift; cmd="kubectl $(quote "$@")"; run=(kubectl "$@") ;;
  --sh) cmd="${2:?pipeline required}"; run=(bash -o pipefail -c "$2") ;;
  *) echo "after <name> expected -- or --sh" >&2; exit 2 ;;
esac

dir="$(dirname "$0")/fixtures"; mkdir -p "$dir"
out="$dir/$name.txt"
if [ "$append" = 0 ]; then
  printf '# captured on k8s-volga (k8s 1.36.2), fixture: %s\n' "$name" > "$out"
fi
{
  [ -n "$title" ] && printf -- '--- %s ---\n' "$title"
  printf '# cmd: %s\n' "${cmd//$'\n'/$'\n'# }"
  "${run[@]}" 2>&1 || echo "# exit code: $?"
} | tee -a "$out"
echo "saved: fixtures/$name.txt"
