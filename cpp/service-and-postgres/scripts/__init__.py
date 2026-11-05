"""Пакет скриптов стенда `cpp/service-and-postgres`.

ПОЧЕМУ ЗДЕСЬ ЕСТЬ КОД. Набор проверок запускается напрямую —
`python3 -m unittest discover -s scripts` — и тогда PYTHONIOENCODING не
выставлен никем. А сообщения самих проверок написаны по-русски и содержат
«≠» и «×»: на консоли cp1251 проверка падала бы UnicodeEncodeError НА
ПЕЧАТИ СОБСТВЕННОГО СООБЩЕНИЯ, то есть ровно в том окружении, ради
которого она и написана. Читатель увидел бы не «что не сошлось», а ошибку
кодировки.

Печатающие скрипты зовут force_utf8() сами (scripts/console.py); здесь то
же самое делается за набор проверок, у которого своей точки входа нет.

ПОЧЕМУ ЗДЕСЬ ЖЕ ПРАВИТСЯ sys.path. `scripts/outcome.py` и
`scripts/console.py` этого стенда — тонкие переходники к родовому модулю
`cpp/_common` (см. его докстринг): переходники видны по имени `scripts`,
которое ждут все существующие импорты, а сама логика живёт один раз на
уровень выше стенда. Чтобы `from cpp._common import ...` разрешался,
каталог, который СОДЕРЖИТ `cpp/` (родитель `cpp/service-and-postgres` на
два уровня вверх), обязан быть в sys.path — это и делается здесь, до
всех остальных импортов пакета.
"""

import os
import sys

_STAND_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_REPO_ROOT = os.path.dirname(os.path.dirname(_STAND_DIR))
if _REPO_ROOT not in sys.path:
    sys.path.insert(0, _REPO_ROOT)

from scripts.console import force_utf8

force_utf8()
