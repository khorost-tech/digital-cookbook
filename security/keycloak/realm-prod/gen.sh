#!/usr/bin/env bash
# Генерация production-варианта realm-импорта из канонического realm/demo-realm.json.
#
# Зачем отдельный realm для prod-стенда: канонический realm содержит dev-only
# identity provider `mock` (внешний ghcr.io/navikt/mock-oauth2-server для демо
# identity brokering из Task 5) с http-URL. В режиме `start` (production) Keycloak
# валидирует URL брокеров и отклоняет http для realm с sslRequired=external:
#   ERROR: The url [token_url] requires secure connections
# Production-стенд (статья 5, HA) брокеринг не демонстрирует, поэтому derived-realm
# = канонический realm БЕЗ identityProviders/identityProviderMappers. sslRequired
# остаётся external (безопасный дефолт). Единственный источник правды —
# realm/demo-realm.json; этот скрипт лишь снимает dev-брокер.
#
# Перегенерировать после изменения realm/demo-realm.json:
#   bash realm-prod/gen.sh
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$DIR/../realm/demo-realm.json"
OUT="$DIR/demo-realm.json"

python - "$SRC" "$OUT" <<'PY'
import json, sys
src, out = sys.argv[1], sys.argv[2]
d = json.load(open(src, encoding="utf-8"))
removed_ip = [ip.get("alias") for ip in d.get("identityProviders", [])]
removed_map = len(d.get("identityProviderMappers", []))
d.pop("identityProviders", None)
d.pop("identityProviderMappers", None)
with open(out, "w", encoding="utf-8") as f:
    json.dump(d, f, ensure_ascii=False, indent=2)
    f.write("\n")
print(f"prod realm -> {out}")
print(f"removed identityProviders: {removed_ip}; identityProviderMappers: {removed_map}")
PY
