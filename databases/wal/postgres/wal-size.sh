#!/usr/bin/env bash
# Usage: ./wal-size.sh lsn                 → текущий LSN
#        ./wal-size.sh diff <lsn1> <lsn2>  → байт между LSN
set -euo pipefail
psql() { docker compose exec -T postgres psql -U postgres -d waldemo -tAc "$1"; }
case "${1:-lsn}" in
  lsn)  psql "SELECT pg_current_wal_lsn();" ;;
  diff) psql "SELECT pg_wal_lsn_diff('$3','$2');" ;;
  *)    echo "usage: $0 lsn | diff <lsn1> <lsn2>" >&2; exit 2 ;;
esac
