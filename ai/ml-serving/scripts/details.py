"""Человекочитаемый detail строки фикстуры: из измеренного значения, без новых данных."""


def yes(b):
    return "да" if b else "нет"


def describe(key, a, extra):
    if key == "controls":
        return ("сервис отдаёт те же вероятности, что модель в процессе: %s; строка не той ширины — %d, нет поля — %d, "
                "NaN в JSON — %d") % (yes(a["service_matches_in_process"]), a["wrong_width_status"], a["missing_field_status"],
                                      a["nan_row_status"])
    if key == "service_vs_library":
        return "запрос к сервису по HTTP против вызова модели в процессе, по одной строке: медленнее не меньше чем вдвое — %s" % (
            yes(a["http_at_least_2x_in_process"]))
    if key == "cold_start":
        return ("запросы до готовности получают отказ: %s; загрузка модели меньше 20 %% старта: %s; память процессов "
                "сервиса больше 20 артефактов (%.2f МБ): %s") % (
            yes(a["requests_before_ready_refused"]), yes(a["model_load_under_20pct_of_start"]), extra["artifact_mb"],
            yes(a["rss_over_20x_artifact"]))
    if key == "batching":
        return ("генератор не упирается в себя: %s; адаптивный батчинг: при 64 клиентах пропускная способность выше в 1,5 раза "
                "и больше — %s, средняя пачка больше 4 — %s, часть запросов отклонена 503 — %s; при одном клиенте выигрыша нет — %s, "
                "пачка меньше 1,5 — %s; без батчинга отказов нет — %s; доля запросов с ответом 200 не позже 100 мс, отказы — промах: "
                "при одном клиенте батчинг не лучше — %s, при 64 — лучше — %s") % (
            yes(a["generator_ceiling_over_2x_best"]), yes(a["c64_throughput_gain_over_1_5"]), yes(a["c64_mean_batch_over_4"]),
            yes(a["c64_batching_rejects_some"]), yes(a["c1_no_gain"]), yes(a["c1_mean_batch_under_1_5"]),
            yes(a["without_batching_rejects_none"]), yes(a["slo_100ms"]["c1_batching_not_better"]), yes(a["slo_100ms"]["c64_batching_better"]))
    if key == "workers":
        return "генератор не упирается в себя: %s; 4 воркера против 1 при 64 клиентах — больше чем вдвое: %s; отказов нет: %s" % (
            yes(a["generator_ceiling_over_2x_best"]), yes(a["four_workers_over_2x"]), yes(not any(a["rejected"].values())))
    if key == "degradation":
        p = extra["policies"]
        return ("модель отвечает за 200 мс, клиент ждёт 50 мс; %d запросов, мошеннических %d. Ноль вместо оценки: ошибок %d, "
                "флагов %d, поймано %d; запасное правило: ошибок %d, флагов %d, поймано %d; явная ошибка: ошибок %d; "
                "здоровая модель: флагов %d, поймано %d") % (
            extra["requests"], extra["fraud"], p["silent_zero"]["visible_errors"], p["silent_zero"]["flagged"],
            p["silent_zero"]["fraud_caught"], p["heuristic"]["visible_errors"], p["heuristic"]["flagged"],
            p["heuristic"]["fraud_caught"], p["explicit_error"]["visible_errors"], p["healthy"]["flagged"], p["healthy"]["fraud_caught"])
    return ""
