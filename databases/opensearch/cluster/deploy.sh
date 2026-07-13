#!/usr/bin/env bash
# Полный запуск демо-стенда opensearch/cluster.
#
# import_playbook в deploy.yml — статический импорт: Ansible резолвит путь к
# upstream opensearch.yml на этапе ПАРСИНГА, до запуска задач. Поэтому upstream-плейбук
# opensearch-project/ansible-playbook нельзя склонировать «внутри» того же прогона —
# он должен существовать заранее. Этот скрипт клонирует (или обновляет) upstream, а затем
# запускает деплой. Так стенд действительно воспроизводится «с нуля» одной командой.
#
# Предпосылка: demo-сертификаты уже сгенерированы (cd certs && ./gen-self-signed.sh).
# Все аргументы прокидываются в ansible-playbook (например: ./deploy.sh --check).
set -euo pipefail
cd "$(dirname "$0")"

UPSTREAM_DIR=".upstream/ansible-playbook"
UPSTREAM_REPO="https://github.com/opensearch-project/ansible-playbook"

if [ -d "$UPSTREAM_DIR/.git" ]; then
  echo "==> upstream уже склонирован, обновляю ($UPSTREAM_DIR)"
  git -C "$UPSTREAM_DIR" pull --ff-only
else
  echo "==> клонирую upstream-плейбук в $UPSTREAM_DIR"
  git clone --depth 1 "$UPSTREAM_REPO" "$UPSTREAM_DIR"
fi

echo "==> ansible-playbook -i inventory/hosts.ini deploy.yml $*"
exec ansible-playbook -i inventory/hosts.ini deploy.yml "$@"
