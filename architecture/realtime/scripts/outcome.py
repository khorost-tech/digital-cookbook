"""Контракты строк стенда realtime — исходы каждой оси над родовым contract.py.

`error` отделён везде: проба, которая не смогла поставить опыт (контейнер не
поднялся, клиент не подключился), не попадает в таблицу как свойство предмета.
Сверх родовых полей у каждой строки — `cmd`: команда, которой читатель повторит
этот замер; статья ставит её перед листингом.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from contract import Contract  # noqa: E402

_CMD = ("kind", "variant", "probe", "outcome", "detail", "cmd")

# Ось 1: дошли ли все события; величины — байты и запросы.
WIRE = Contract(outcomes=("delivered", "incomplete", "error"), success="delivered", fields=_CMD)
# Ось 2: SSE за прокси — пришли ли события в окне; WS — удалось ли рукопожатие;
# простой — живо ли соединение.
SSE_WINDOW = Contract(outcomes=("delivered", "held", "error"), success="delivered", fields=_CMD)
HANDSHAKE = Contract(outcomes=("upgraded", "refused", "error"), success="upgraded", fields=_CMD)
IDLE = Contract(outcomes=("alive", "closed", "error"), success="alive", fields=_CMD)
# Ось 3: получили ли событие все клиенты всех инстансов.
SCALE = Contract(outcomes=("all", "partial", "error"), success="all", fields=_CMD)
# Ось 4: потеряно ли что-то при разрыве.
# refused — сервер отказал в догоне (503), а не продолжил поток с пропуском.
RECONNECT = Contract(outcomes=("no-loss", "lost", "duplicated", "refused", "mixed", "error"), success="no-loss",
                     fields=_CMD)
# Ось 4, отмена: клиент ушёл, пока сервер читал историю, — снят ли подписчик.
CANCEL = Contract(outcomes=("released", "leaked", "error"), success="released", fields=_CMD)
# Ось 4, шторм: переподключения после рестарта инстанса — всплеском в одной
# корзине 100 мс (burst) или распределены (spread).
STORM = Contract(outcomes=("spread", "burst", "error"), success="spread", fields=_CMD)
# Ось 5: что сервер сделал с медленным клиентом — копил очередь (grew), выбрасывал
# (dropped), отключил (disconnected) или вовсе не заметил его (unnoticed: события
# забрал прокси). Быстрый клиент рядом обязан получить всё — это контроль, а не исход.
SLOW = Contract(outcomes=("grew", "dropped", "disconnected", "unnoticed", "error"), success="grew",
                fields=_CMD)

END_MARKER = WIRE.END_MARKER
