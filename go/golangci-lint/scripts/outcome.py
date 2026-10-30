"""Контракты строк стенда golangci-lint — исходы каждой оси над родовым contract.py.

`error` отделён везде: прогон, который не смог поставить опыт (линтер упал,
конфиг не прошёл схему), не попадает в таблицу как свойство линтера. У каждой
строки — `cmd`: команда, которой читатель повторит замер.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from contract import Contract  # noqa: E402

_CMD = ("kind", "variant", "probe", "outcome", "detail", "cmd")

# Оси 1–2: находки прогона, сверенные с разметкой. clean — контроль без находок.
DEFAULT = Contract(outcomes=("scored", "clean", "error"), success="scored", fields=_CMD)
# Ось 3: сколько находок показал вывод из реально найденных.
TRUNCATE = Contract(outcomes=("all-shown", "truncated", "error"), success="all-shown", fields=_CMD)
# Ось 4: какие размеченные находки спрятало исключение.
EXCLUDE = Contract(outcomes=("hidden", "nothing-hidden", "error"), success="hidden", fields=_CMD)
# Ось 5: что подавлено и что отметил nolintlint.
NOLINT = Contract(outcomes=("scored", "error"), success="scored", fields=_CMD)
# Ось 6: какие находки показал прогон по диапазону git.
LEGACY = Contract(outcomes=("scored", "error"), success="scored", fields=_CMD)
# Ось 7: свой анализатор и готовые линтеры на одной разметке.
CUSTOM = Contract(outcomes=("scored", "error"), success="scored", fields=_CMD)
# Ось 8: дефект разметки подтверждён поведением (go test).
SEMANTICS = Contract(outcomes=("confirmed", "error"), success="confirmed", fields=_CMD)
# Тренажёр: красный старт, зелёный эталон.
TRAINER = Contract(outcomes=("red", "green", "error"), success="green", fields=_CMD)

END_MARKER = DEFAULT.END_MARKER
