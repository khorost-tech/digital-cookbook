"""PostgreSQL: как длину и уникальность видит база, а не язык."""

import re
import subprocess
import time

from langs import image, sh

NAME = "text-unicode-pg"


def up():
    sh(["docker", "rm", "-f", NAME], check=False)
    sh(["docker", "run", "-d", "--name", NAME, "-e", "POSTGRES_PASSWORD=local-fixture-only", image("postgres")])
    for _ in range(60):
        if sh(["docker", "exec", NAME, "pg_isready", "-U", "postgres"], check=False).returncode == 0:
            time.sleep(1)
            return
        time.sleep(1)
    raise RuntimeError("PostgreSQL не поднялся")


def down():
    sh(["docker", "rm", "-f", NAME], check=False)


def sql(q):
    """(SQLSTATE или None при успехе, вывод). VERBOSITY verbose — код ошибки в тексте: «ERROR:  22001: …».
    Отказ засчитывается только с нужным кодом: любая другая ошибка — не доказательство ограничения."""
    p = subprocess.run(["docker", "exec", NAME, "psql", "-U", "postgres", "--set=VERBOSITY=verbose", "-tAc", q],
                       capture_output=True, text=True)
    out = (p.stdout + p.stderr).strip()
    m = re.search(r"ERROR:\s+([0-9A-Z]{5}):", out)
    return (m.group(1) if m else ("XX000" if p.returncode else None)), out


def chars(cps):
    """Строковый литерал PostgreSQL из code points — U&'\\+XXXXXX', без зависимости от кодировки терминала."""
    return "U&'" + "".join("\\+%06X" % c for c in cps) + "'"


def database(v):
    up()
    try:
        fam, e_nfc, e_nfd = (chars(v["strings"][k]) for k in ("family", "e_nfc", "e_nfd"))
        cafe_nfc, cafe_nfd = chars(v["security"]["cafe_nfc"]), chars(v["security"]["cafe_nfd"])
        _, lengths = sql("SELECT char_length(%s) || ',' || octet_length(%s)" % (fam, fam))
        cl, ol = map(int, lengths.split(","))
        ddl = [sql(q)[0] for q in ("CREATE TABLE five (x varchar(5))", "CREATE TABLE one (x varchar(1))", "CREATE TABLE raw (name text UNIQUE)",
                                   "CREATE TABLE norm (name text)", "CREATE UNIQUE INDEX ON norm (normalize(name, NFC))")]
        fam5 = sql("INSERT INTO five VALUES (%s)" % fam)[0]
        e1_nfc = sql("INSERT INTO one VALUES (%s)" % e_nfc)[0]
        e1_nfd = sql("INSERT INTO one VALUES (%s)" % e_nfd)[0]
        raw_first, raw_second = sql("INSERT INTO raw VALUES (%s)" % cafe_nfc)[0], sql("INSERT INTO raw VALUES (%s)" % cafe_nfd)[0]
        n_first, n_second = sql("INSERT INTO norm VALUES (%s)" % cafe_nfc)[0], sql("INSERT INTO norm VALUES (%s)" % cafe_nfd)[0]
        # отрицательный контроль классификатора: синтаксическая ошибка не должна сойти за нарушение ограничения
        broken = sql("INSERT INTO norm VALUES (%s" % cafe_nfd)[0]
        version = sql("SHOW server_version")[1]
    finally:
        down()
    return {"actual": {"char_length_counts_code_points": cl == len(v["strings"]["family"]), "octet_length_counts_bytes": ol == 25,
                       "ddl_succeeded": all(x is None for x in ddl),
                       "varchar5_rejects_family_22001": fam5 == "22001", "varchar1_accepts_nfc_e": e1_nfc is None,
                       "varchar1_rejects_nfd_e_22001": e1_nfd == "22001",
                       "unique_raw_accepts_both_forms": raw_first is None and raw_second is None,
                       "unique_on_normalize_rejects_second_23505": n_first is None and n_second == "23505",
                       "unexpected_sqlstate_detected": broken not in (None, "22001", "23505")},
            "family": {"char_length": cl, "octet_length": ol}, "server_version": version,
            "sqlstate": {"varchar5_family": fam5, "varchar1_nfd": e1_nfd, "unique_normalized_second": n_second, "broken_insert": broken}}
