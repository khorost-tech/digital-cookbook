"""Контракты строк стенда — предметная часть над `cpp/_common/outcome.py`.

У каждой оси свои исходы (спека: «контракт строки общий, со своими исходами у
каждой оси»); `error` отделён везде — «проба сломалась» не попадает в таблицу
как свойство предмета. Сверх родовых полей каждая строка несёт `cmd` — точную
команду, давшую вывод: статья ставит её перед листингом.
"""

from cpp._common.outcome import Contract

_CMD = ("kind", "variant", "probe", "outcome", "detail", "cmd")

# Пробы инструментов до ожиданий: нашёл ли инструмент то, что ищет проба.
PROBE = Contract(outcomes=("observed", "absent", "error"), success="observed", fields=_CMD)
# Ось 1: поймал ли детектор засеянный дефект.
DEFECTS = Contract(outcomes=("caught", "missed", "mixed", "error"), success="caught", fields=_CMD)
# Ось 2: осталась ли измеряемая работа в теле бенчмарка. replaced — умножения
# есть, но цикла по ним нет: компилятор заменил цикл формулой.
BENCH = Contract(outcomes=("kept", "replaced", "lost", "error"), success="kept", fields=_CMD)
# Оси 3 и 5: обслужен ли запрос. mixed — вариант повторён несколько раз, и
# часть повторов дослужена, часть нет.
SERVED = Contract(outcomes=("served", "refused", "mixed", "error"), success="served", fields=_CMD)
# Ось 4: что сделал сервис на конфигурации.
CONFIG = Contract(outcomes=("served", "refused", "started-silently", "failed-later", "error"),
                  success="served", fields=_CMD)

END_MARKER = PROBE.END_MARKER


def read_measurements(path):
    """Строки величин без исхода: `kind`, `variant` непусты, `cmd` обязателен."""
    rows = PROBE.read_measurements(path, value_fields=("kind", "variant"))
    for row in rows:
        if not row.get("cmd"):
            raise ValueError("%s: у строки %r нет команды" % (path, row))
    return rows
