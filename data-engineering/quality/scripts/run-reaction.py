"""Ось 2: что доходит до витрины при разной реакции на провал проверки.

Партия mixed: 15 плохих строк из 287 (пустой клиент, отрицательная сумма,
неизвестный статус). Режимы:
  fail       — тесты с severity error: провал останавливает построение витрины;
  warn       — тесты с severity warn: витрина строится;
  quarantine — построчные правила: хорошие строки в витрину, плохие в карантин;
  split-naive / split-fixed — партия с пустым статусом и пустой суммой
               (nullfields): куда попадает каждая строка при флаге без
               coalesce и с ним; контроль сохранения — валидные строки до
               бизнес-фильтров и агрегации + карантин = все входные строки;
  fail-kept  — fail поверх витрины прошлой успешной загрузки (чистая партия):
               витрина не пересобрана и отдаёт прошлую версию — что видит
               потребитель, когда остановка случилась не на пустом месте.
Контроль — чистая партия во всех режимах. Счёт: заказов (не отменённых) в
витрине против хороших по эталону, строк в карантине.

    python3 scripts/run-reaction.py              → все варианты, фикстура
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import END_MARKER, REACTION  # noqa: E402

VARIANTS = ("clean-fail", "clean-quarantine", "fail", "warn", "quarantine", "fail-kept", "split-naive", "split-fixed")
TESTS = ("stg_customers", "stg_orders", "amount_range", "test_type:generic")


def count(q):
    try:
        return int(stack.sql(q)[0][0] or 0)
    except SystemExit:  # таблицы нет — витрина не построена
        return None


def mart_state():
    return stack.sql("SELECT coalesce(sum(orders), 0), coalesce(sum(revenue), 0) FROM marts.revenue")[0]


def measure_kept():
    """Прошлая загрузка (чистая) построила витрину; новая партия с браком — fail."""
    stack.sql("DROP SCHEMA IF EXISTS staging CASCADE; DROP SCHEMA IF EXISTS marts CASCADE; "
              "DROP SCHEMA IF EXISTS quarantine CASCADE")
    vars_ = '{"severity": "error"}'
    stack.gen("load", "clean")
    rc_prev, _ = stack.dbt("build", "--select", *TESTS, "revenue", "--vars", vars_, check=False)
    before = mart_state()
    stack.gen("load", "mixed")
    bad = set(stack.gen("bad", "mixed")["bad_ids"])
    good = count("SELECT count(*) FROM raw.orders WHERE status <> 'cancelled' AND id NOT IN (%s)"
                 % ",".join(map(str, bad)))
    rc, _ = stack.dbt("build", "--select", *TESTS, "revenue", "--vars", vars_, check=False)
    after = mart_state()
    unchanged = after == before
    r = {"probe": "batch clean, then mixed", "mode": "fail", "defect": "mixed", "bad": len(bad),
         "good_noncancelled": good, "prev_dbt_rc": rc_prev, "dbt_rc": rc, "mart_built": True,
         "mart_rebuilt": not unchanged, "mart_orders": int(after[0]), "prev_mart_orders": int(before[0]),
         "bad_in_mart": 0, "good_missing": 0, "quarantined": None}
    r["outcome"] = "mismatch" if unchanged and rc else "match"
    r["detail"] = "сборка %s; витрина %s: заказов %d — прошлая загрузка %d, хороших в новой партии %d" % (
        "остановлена" if rc else "прошла", "не пересобрана, осталась от прошлой загрузки" if unchanged
        else "пересобрана", r["mart_orders"], r["prev_mart_orders"], good)
    return r


def measure_split(name):
    """Разделение партии с NULL: сколько строк ушло в витрину, в карантин и никуда."""
    stack.sql("DROP SCHEMA IF EXISTS staging CASCADE; DROP SCHEMA IF EXISTS marts CASCADE; "
              "DROP SCHEMA IF EXISTS quarantine CASCADE")
    stack.gen("load", "nullfields")
    bad = set(stack.gen("bad", "nullfields")["bad_ids"])
    view = "staging.stg_orders_flagged" + ("_naive" if name == "split-naive" else "")
    rc, _ = stack.dbt("run", "--select", "+revenue_valid", "+orders_quarantine", "stg_orders_flagged_naive",
                      check=False)
    total = count("SELECT count(*) FROM raw.orders")
    # тот же отбор, что у моделей: витрина — where is_valid, карантин — where not is_valid
    to_mart = count("SELECT count(*) FROM %s WHERE is_valid" % view)
    to_quarantine = count("SELECT count(*) FROM %s WHERE NOT is_valid" % view)
    lost = total - to_mart - to_quarantine
    bad_quarantined = count("SELECT count(*) FROM %s WHERE NOT is_valid AND id IN (%s)"
                            % (view, ",".join(map(str, bad))))
    # витрина revenue_valid дальше отбрасывает отменённые и агрегирует: инвариант —
    # на разделении, до бизнес-фильтров
    mart = count("SELECT sum(orders) FROM marts.revenue_valid") if name == "split-fixed" else None
    r = {"probe": "batch nullfields", "mode": "quarantine", "defect": "nullfields", "bad": len(bad), "dbt_rc": rc,
         "rows": total, "to_mart": to_mart, "to_quarantine": to_quarantine, "lost": lost, "mart_orders": mart,
         "bad_quarantined": bad_quarantined, "quarantined": count("SELECT count(*) FROM quarantine.orders_quarantine")
         if name == "split-fixed" else None}
    r["outcome"] = "match" if lost == 0 and bad_quarantined == len(bad) else "mismatch"
    r["detail"] = "строк %d: валидных до бизнес-фильтров %d, в карантин %d, никуда %d; плохих %d, из них в карантине %d" % (
        total, to_mart, to_quarantine, lost, len(bad), bad_quarantined)
    return r


def measure(name):
    if name == "fail-kept":
        return measure_kept()
    if name.startswith("split"):
        return measure_split(name)
    defect = "clean" if name.startswith("clean") else "mixed"
    mode = name.split("-")[-1]
    stack.sql("DROP SCHEMA IF EXISTS staging CASCADE; DROP SCHEMA IF EXISTS marts CASCADE; "
              "DROP SCHEMA IF EXISTS quarantine CASCADE")
    stack.gen("load", defect)
    bad = set(stack.gen("bad", defect)["bad_ids"])
    good = count("SELECT count(*) FROM raw.orders WHERE status <> 'cancelled'%s" % (
        " AND id NOT IN (%s)" % ",".join(map(str, bad)) if bad else ""))
    if mode == "quarantine":
        # правила здесь — построчно в модели stg_orders_flagged; тесты staging не запускаются,
        # иначе их провал с severity error пропустил бы и карантин, и витрину
        rc, _ = stack.dbt("run", "--select", "+revenue_valid", "+orders_quarantine", check=False)
        mart = count("SELECT sum(orders) FROM marts.revenue_valid")
        quarantined = count("SELECT count(*) FROM quarantine.orders_quarantine")
    else:
        vars_ = '{"severity": "%s"}' % ("warn" if mode == "warn" else "error")
        rc, _ = stack.dbt("build", "--select", *TESTS, "revenue", "--vars", vars_, check=False)
        mart = count("SELECT sum(orders) FROM marts.revenue")
        quarantined = None
    r = {"probe": "batch %s" % defect, "mode": mode, "defect": defect, "bad": len(bad), "good_noncancelled": good,
         "dbt_rc": rc, "mart_built": mart is not None, "mart_orders": mart or 0,
         "bad_in_mart": (mart - good) if mart is not None else 0,
         "good_missing": (good - mart) if mart is not None and mart < good else (good if mart is None else 0),
         "quarantined": quarantined}
    ok = r["mart_built"] and r["bad_in_mart"] == 0 and r["good_missing"] == 0
    r["outcome"] = "match" if ok else "mismatch"
    r["detail"] = "витрина %s: заказов %d, хороших по эталону %d; плохих в витрине %d; в карантине %s" % (
        "построена" if r["mart_built"] else "не построена", r["mart_orders"], good, r["bad_in_mart"],
        "—" if quarantined is None else quarantined)
    return r


if __name__ == "__main__":
    common.run("reaction", measure, VARIANTS, REACTION, END_MARKER)
