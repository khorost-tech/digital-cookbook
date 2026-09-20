"""Раннер проб на psycopg — штатном драйвере PostgreSQL для Python.

Печатает строки "<id>\tOK|FAIL\t<сообщение>". После неудачной пробы соединение
пересоздаётся: psycopg переводит сессию в состояние ошибки, и без пересоздания
все последующие пробы вернули бы FAIL по инерции — матрица выглядела бы
правдоподобно и была бы ложной.
"""

import os
import sys

import psycopg

DSN = os.environ.get(
    "PICO_DSN", "postgres://admin:Picodata1@picodata-1:5432/picodata?sslmode=disable"
)
PROBES = sys.argv[1] if len(sys.argv) > 1 else "/probes.tsv"
MSG_LIMIT = 160


def one_line(text: object) -> str:
    return " ".join(str(text).split())[:MSG_LIMIT]


def main() -> None:
    conn = psycopg.connect(DSN, autocommit=True)
    try:
        with open(PROBES, encoding="utf-8") as f:
            for line in f:
                line = line.rstrip("\n")
                if not line or line.startswith("#"):
                    continue
                parts = line.split("\t", 2)
                if len(parts) < 3:
                    continue
                probe_id, sql = parts[0], parts[2]
                try:
                    with conn.cursor() as cur:
                        cur.execute(sql)
                    print(f"{probe_id}\tOK\t", flush=True)
                except Exception as exc:  # noqa: BLE001 — нужен любой отказ, с текстом
                    print(f"{probe_id}\tFAIL\t{one_line(exc)}", flush=True)
                    conn.close()
                    conn = psycopg.connect(DSN, autocommit=True)
    finally:
        conn.close()


if __name__ == "__main__":
    main()
