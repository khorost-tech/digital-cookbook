#!/usr/bin/env bash
# ДЕМО-СКРИПТ: воспроизводимая цепочка nsc operator -> account -> user
# для decentralized JWT auth. Все ключи — учебные, генерируются заново
# при каждом запуске в локальный store (nsc/stores, nsc/keys — в .gitignore).
#
# Использование:
#   cd nats/05-security
#   NSC=/path/to/nsc bash nsc/setup.sh
#
# По умолчанию использует `nsc` из PATH; при установке в нестандартный путь
# (как в этом окружении — C:/Users/ak/go/bin/nsc) передать через переменную:
#   NSC="C:/Users/ak/go/bin/nsc" bash nsc/setup.sh
#
# Результат:
#   - operator DEMO, аккаунты APP_A / APP_B (+ системный SYS), пользователи
#     alice (APP_A) и bob (APP_B) — всё в изолированном XDG_DATA_HOME под
#     nats/05-security/nsc/nsc-data, не трогает системный ~/.local/share/nats.
#   - публичный stream-export APP_A: events.public.>
#   - импорт APP_B: events.public.> из APP_A -> локально from-a.events.>
#   - resolver.generated.conf (MEMORY resolver, JWT встроены) в корне 05-security/
#     — рабочий конфиг сервера, НЕ коммитится (см. .gitignore).

set -euo pipefail

NSC="${NSC:-nsc}"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"

# Полная изоляция от глобального окружения nsc пользователя: и данные
# (ключи/creds), и мелкое состояние "текущий operator/account" (nsc.json)
# уходят в локальный каталог этого демо, а не в ~/.config/nats и
# ~/.local/share/nats. Так повторные запуски скрипта не путают глобальный
# контекст nsc на машине разработчика с демо-окружением этого стенда.
export XDG_DATA_HOME="$HERE/nsc-data"
export XDG_CONFIG_HOME="$HERE/nsc-data/config"
mkdir -p "$XDG_DATA_HOME" "$XDG_CONFIG_HOME"

echo "== nsc store: $XDG_DATA_HOME =="

echo "== 1/6: operator DEMO (+ системный аккаунт SYS) =="
"$NSC" add operator --generate-signing-key --sys --name DEMO

echo "== 2/6: аккаунты APP_A и APP_B =="
"$NSC" add account --name APP_A
"$NSC" add account --name APP_B

echo "== 3/6: пользователи alice (APP_A) и bob (APP_B) =="
"$NSC" add user --account APP_A --name alice
"$NSC" add user --account APP_B --name bob

echo "== 4/6: публичный export на APP_A: events.public.> =="
"$NSC" add export --account APP_A --name public-events --subject "events.public.>"

# nsc --field --output-file вместо чтения из stdout/pipe: на Windows/Git Bash
# вывод части команд nsc уходит напрямую в консоль мимо обычного stdout,
# и обычный пайп/redirect читает пустоту. --output-file пишет по-настоящему.
ACC_A_KEY_FILE="$HERE/.acc-a-key.tmp"
"$NSC" describe account --name APP_A --field sub --output-file "$ACC_A_KEY_FILE" > /dev/null
ACC_A_KEY=$(tr -d '"' < "$ACC_A_KEY_FILE")
rm -f "$ACC_A_KEY_FILE"

echo "== 5/6: импорт на APP_B: events.public.> из APP_A -> from-a.events.> =="
"$NSC" add import --account APP_B \
  --src-account "$ACC_A_KEY" \
  --remote-subject "events.public.>" \
  --local-subject "from-a.events.>"

echo "== 6/6: генерация серверного resolver-конфига (MEMORY, JWT встроены) =="
"$NSC" generate config --mem-resolver --config-file "$ROOT/resolver.generated.conf" --force

echo
echo "Готово. Creds-файлы пользователей (НЕ коммитить, .gitignore уже покрывает *.creds):"
find "$XDG_DATA_HOME" -name '*.creds'
echo
echo "Серверный конфиг: $ROOT/resolver.generated.conf (НЕ коммитить)"
echo "Запуск сервера с ним — см. README, раздел 'Проверка decentralized JWT auth'."
