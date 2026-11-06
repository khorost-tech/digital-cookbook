"""Человекочитаемый detail строки фикстуры: из измеренного значения, без новых данных."""


def yes(b):
    return "да" if b else "нет"


def describe(key, a, extra):
    if key == "controls":
        f = extra["fraud_by_scheme"]
        return ("пользователей %d, дней %d, платежей %d; мошеннических: захват аккаунта %d, проверка карты %d, шум %d; "
                "доля мошенничества до дня 105 — %.2f %%; два прогона генератора совпали побайтно: %s") % (
            a["users"], a["days"], extra["transactions"], f["takeover"], f["card_testing"], f["noise"],
            100 * extra["fraud_share_before_105"], yes(a["deterministic"]))
    if key == "reference":
        return "эталон признаков против перебора на %d платежах: расхождений %d; окно со своим платежом поймано: %s" % (
            a["sampled"], a["mismatches"], yes(a["broken_window_detected"]))
    if key == "labels":
        return ("к концу дня 119 размечено %.2f %% платежей дней 90–119: мошеннических %d, легитимных %d") % (
            100 * extra["prod_labeled_share"], extra["prod_fraud_labels_known"], a["prod_legit_labels_known"])
    if key == "model":
        return "два обучения дали одинаковые предсказания: %s; AUC на валидации %.3f, порог флага %.4f (1 %% валидации)" % (
            yes(a["deterministic"]), extra["val_auc"], extra["threshold"])
    return ""
