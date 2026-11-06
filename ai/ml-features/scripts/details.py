"""Человекочитаемый detail строки фикстуры: из измеренного значения, без новых данных."""

VARIANTS = {"as_written": "как написан", "fix_current": "без своего платежа в окне",
            "fix_current_calendar": "плюс скользящие 30 дней", "all_fixed": "плюс пропуск как пропуск"}


def yes(b):
    return "да" if b else "нет"


def pct(x):
    return "%.2f %%" % (100 * x)


def describe(key, a, extra):
    if key == "controls":
        return "батч SQL против эталона: расхождений %d; сравнение ловит сдвиг на единицу: %s; PostgreSQL %d" % (
            a["batch_vs_reference_mismatches"], yes(a["comparator_detects_injection"]), a["postgres_major"])
    if key == "skew":
        parts = []
        for k, label in VARIANTS.items():
            s, d = extra["mismatch_share"][k], extra["decisions"][k]
            parts.append("%s — расходится tx_count_24h %s, amount_avg_30d %s; перевёрнуто решений %d, поймано мошенничества %d из %d (батч %d)" % (
                label, pct(s["tx_count_24h"]), pct(s["amount_avg_30d"]), d["flipped"], d["fraud_caught_online"],
                extra["fraud_requests"], d["fraud_caught_batch"]))
        return "запросов %d, ошибок %d; " % (extra["requests"], a["errors"]) + "; ".join(parts)
    if key == "feature_store":
        o, s = extra["mismatch_share_own_row"], extra["mismatch_share_shifted"]
        return ("граница PIT включительна: %s; отметка = время платежа — расходится с онлайном tx_count_24h %s, остальные %s, "
                "перевёрнуто %d из %d; отметка на секунду раньше — расхождений вне ttl %d, с паузой больше ttl %d случаев, "
                "расходятся все: %s; значение Feast отличается от эталона «до платежа» в %s запросов; "
                "онлайн: известный пользователь найден — %s, неизвестный пуст — %s") % (
            yes(a["inclusive_boundary"]), pct(o["tx_count_24h"]), pct(o["amount_avg_30d"]), extra["own_row_flipped"],
            extra["requests"], a["shifted"]["mismatch_outside_ttl_gap"], extra["ttl_gap_cases"],
            yes(a["shifted"]["ttl_gap_cases_all_differ"]), pct(extra["not_reference_share"]),
            yes(a["online_controls"]["known_user_found"]), yes(a["online_controls"]["unknown_user_null"]))
    if key == "materialization":
        i = extra["intervals"]
        return "запросов %d; устаревших значений против записи каждого состояния: " % extra["requests"] + ", ".join(
            "раз в %s — %s, перевёрнуто %d" % (k.replace("m", " мин").replace("h", " ч"), pct(i[k]["stale_share"]), i[k]["flipped"])
            for k in ("15m", "1h", "24h"))
    if key == "point_in_time":
        r = extra["auc"]
        return ("AUC валидация → прод: снимок на день 90 %.3f → %.3f, PIT по времени платежа с самим платежом %.3f → %.3f, "
                "строго по предыдущим %.3f → %.3f, PIT по времени chargeback %.3f → %.3f; счёт с самим платежом у мошеннических "
                "строк обучения на единицу больше: %s; «строго по предыдущим» против независимого счёта на всех строках — "
                "расхождений %d; PIT Feast против эталона: расхождений %d") % (
            r["naive"]["val_auc"], r["naive"]["prod_auc"], r["by_event"]["val_auc"], r["by_event"]["prod_auc"],
            r["by_event_prior"]["val_auc"], r["by_event_prior"]["prod_auc"],
            r["by_label"]["val_auc"], r["by_label"]["prod_auc"], yes(a["by_event_includes_own_label"]),
            a["prior_vs_reference_mismatches_all_rows"], a["feast_label_pit_vs_reference_mismatches"])
    if key == "one_path":
        return "одна SQL-функция на оба пути, запросов %d: расхождений с батчем %d, обучения с инференсом %d" % (
            extra["requests"], a["mismatch_vs_batch"], a["mismatch_train_vs_serve"])
    return ""
