"""Контракт строки, которую печатает любая проба этого стенда.

Родовая часть контракта (форма строки, маркер завершения, запрет на
пустые именованные поля) вынесена в `cpp/_common/outcome.py` — читай её
докстринг: это тот же контракт, что стенд волны A перенял и усилил
находками ревью, и следующая волна имеет право получить усиление даром,
а не набивать ту же шишку заново. Здесь остаётся только предметная
часть: какие исходы у ЭТОГО стенда и какой из них успешный.
"""

from cpp._common.outcome import Contract

_contract = Contract(
    outcomes=("served", "refused", "error"),
    success="served",
)

OUTCOMES = _contract.OUTCOMES
FIELDS = _contract.FIELDS
NAMED_FIELDS = _contract.NAMED_FIELDS
SUCCESS = _contract.SUCCESS
END_MARKER = _contract.END_MARKER

parse_line = _contract.parse_line
read_fixture = _contract.read_fixture


def read_measurements(path):
    """Строки без исхода: числа соединений, подготовленных запросов, вызовов.

    `probe` у строки величины нет — это и есть различие двух чтений этого
    стенда; `kind` и `variant` обязаны быть непустыми ровно по той же
    причине, что и у строк с исходом.
    """
    return _contract.read_measurements(path, value_fields=("kind", "variant"))
