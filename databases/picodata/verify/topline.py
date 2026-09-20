"""Приводит JSON-ответ плагина к тем же строкам, что печатает psql: id|title|views.

Отдельным файлом, а не строкой внутри shell: во вложенных кавычках f-строка
превращается в нечитаемое экранирование, а ошибку в ней видно только в рантайме.
"""

import json
import sys

for row in json.load(sys.stdin)["items"]:
    print("{}|{}|{}".format(row["id"], row["title"], row["views"]))
