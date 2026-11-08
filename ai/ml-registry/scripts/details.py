"""Человекочитаемый detail строки фикстуры: из измеренного значения, без новых данных."""


def yes(b):
    return "да" if b else "нет"


def describe(key, a, extra):
    if key == "controls":
        return ("модель из реестра предсказывает как в памяти: %s; несуществующий алиас — %s; артефакт по умолчанию — %s; "
                "сохранение без skops_trusted_types — %s") % (
            yes(a["registry_roundtrip_identical"]), a["missing_alias"], ", ".join(a["default_artifact"]), a["log_without_trusted_types"])
    if key == "five_parts":
        s, f = a["same_inputs"], extra["flipped"]
        return ("на %d запросах прода: те же входы — предсказания совпали: %s, другие байты у %s; срез на день больше — "
                "перевёрнуто %d, дайджест данных другой: %s; признак за 14 дней вместо 30 при старом в сервинге — %d; "
                "препроцессинг отдельно от модели — %d, внутри пайплайна — %d") % (
            extra["prod_requests"], yes(s["predictions_identical"]), ", ".join(s["differing_files"]), f["data_slice"],
            yes(a["data_slice"]["digest_differs"]), f["feature_version"], f["preprocessing_separate"], a["preprocessing"]["pipeline_flipped"])
    if key == "decision_policy":
        return ("та же модель на %d запросах прода: порог 1 %% флагов по валидации 75–89 — %.4f, по окну 60–74 — %.4f; "
                "перевёрнуто решений %d") % (extra["prod_requests"], extra["threshold_val"], extra["threshold_older"], extra["flipped"])
    if key == "library_version":
        parts = []
        for v, r in a["by_version"].items():
            parts.append("%s: с тем же списком доверия — %s, с типами scikit-learn из сообщения — %s, предсказания совпали: %s, "
                         "InconsistentVersionWarning: %s" % (v, r["same_trust_list"], r["outcome"], yes(r["predictions_identical"]),
                                                             yes(r["version_warning"])))
        return "модель из scikit-learn 1.9.1; " + "; ".join(parts) + "; версия закреплена в requirements.txt артефакта: " + yes(a["artifact_pins_sklearn"])
    if key == "registry":
        return "алиас разрешается: %s; стадии работают: %s, предупреждение — %s" % (
            yes(a["alias_resolves"]), yes(a["stage_api_works"]), ", ".join(a["stage_api_warning"]))
    if key == "stages":
        r = a["reconcile"]
        return ("алиас champion перенесён на v2, реестр говорит v2; запущенный mlflow models serve отдаёт v1: %s, v2: %s; "
                "после перезапуска — v2: %s; /version отдаёт версию MLflow: %s; сверка «загружено против алиаса»: до переноса — %s, "
                "после — %s, после перезапуска — %s; у прежней версии после переноса след алиаса есть: %s") % (
            yes(a["after_move_serves_v1"]), yes(a["after_move_serves_v2"]), yes(a["after_restart_serves_v2"]),
            yes(a["version_endpoint_is_mlflow_version"]), yes(r["before_move"]), yes(r["after_move"]), yes(r["after_restart"]), yes(a["old_version_alias_trace"]))
    if key == "data_versioning":
        b, g = extra["bytes"], extra["git_bytes~"]
        mb = lambda x: "%.1f МБ" % (x / 1e6)  # noqa: E731
        return ("датасет %s; DVC: кеш после двух версий %s (изменённый файл целиком, неизменный — один раз), git с указателями %s; "
                "откат восстановил байты: %s, модель из них совпала: %s; тот же датасет прямо в git — %s, после gc %s") % (
            mb(b["dataset"]), mb(b["dvc_cache"]), "%.0f КБ" % (g["with_dvc"] / 1e3), yes(a["restored_bytes_identical"]),
            yes(a["restored_model_identical"]), mb(g["plain_loose"]), mb(g["plain_after_gc"]))
    return ""
