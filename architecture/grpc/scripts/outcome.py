"""Контракты строк стенда grpc — исходы каждой оси над родовым contract.py.

`error` отделён везде: проба, которая не смогла поставить опыт, не попадает в
таблицу как свойство предмета. У каждой строки — `cmd`: команда, которой
читатель повторит замер.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from contract import Contract  # noqa: E402

_CMD = ("kind", "variant", "probe", "outcome", "detail", "cmd")

# Ось 1: бросило ли нижнее звено работу, когда клиент ушёл.
# completed — контроль: срок больше работы, исполнитель обязан доделать.
DEADLINE = Contract(outcomes=("stopped", "wasted", "completed", "error"), success="stopped", fields=_CMD)
# Ось 2: перестал ли сервер слать стрим после отмены.
CANCEL = Contract(outcomes=("stopped", "kept-sending", "error"), success="stopped", fields=_CMD)
# Оси 3 и 8: какой код увидел клиент — исход описывает сам код.
ERRORS = Contract(outcomes=("observed", "error"), success="observed", fields=_CMD)
# Ось 4: что увидели интерсепторы.
CHAIN = Contract(outcomes=("observed", "error"), success="observed", fields=_CMD)
# Ось 5: сколько попыток дошло до сервера.
RETRY = Contract(outcomes=("observed", "error"), success="observed", fields=_CMD)
# Ось 6: пережил ли вызов keepalive и простой.
# dropped — активный вызов оборван (молчащий стрим за прокси).
KEEPALIVE = Contract(outcomes=("survived", "goaway", "reconnected", "dropped", "error"), success="survived",
                     fields=_CMD)
# Ось 7: распределились ли вызовы по бэкендам.
BALANCE = Contract(outcomes=("spread", "pinned", "error"), success="spread", fields=_CMD)
# Ось 9: прошёл ли большой ответ.
SIZE = Contract(outcomes=("delivered", "rejected", "error"), success="delivered", fields=_CMD)
PROTOCOL = Contract(outcomes=("same", "differs", "error"), success="same", fields=_CMD)

END_MARKER = DEADLINE.END_MARKER
