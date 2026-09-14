#!/usr/bin/env bash
# Наполняет очередь N джобами. По умолчанию 20.
set -euo pipefail
N="${1:-20}"
docker exec -i bj-postgres psql -U jobs -d jobs -v ON_ERROR_STOP=1 <<SQL
INSERT INTO jobs (kind, payload)
SELECT 'email', jsonb_build_object('to', 'user' || g || '@example.com')
FROM generate_series(1, ${N}) g;
SQL
echo "seed: поставлено ${N} джоб"
docker exec bj-postgres psql -U jobs -d jobs -t -c "SELECT state, count(*) FROM jobs GROUP BY state ORDER BY state"
