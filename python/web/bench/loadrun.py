"""Запуск нагрузчика с отбраковкой кругов, в которых мерили не цель.

Нагрузчик сообщает idle_fraction — долю окна, в которую ни одно соединение
не ждало ответа. У здорового круга это 0.002–0.005. Изредка (примерно один
круг из десяти) появляется круг с 0.4: занятое время и задержки нормальные,
а окно вдвое длиннее — нагрузчик стоял. Такой круг мерил заминку клиента.

Усреднять его с остальными нельзя: он занижает пропускную способность цели,
которая ни при чём. Выбрасывать молча тоже нельзя — отбор без учёта
превращается в подгонку. Поэтому круг перезапускается, а в строку фикстуры
кладётся, сколько попыток было отброшено и с какими значениями.
"""

import json
import subprocess

# Порог с большим запасом: здоровые круги на порядок ниже, больные — на
# порядок выше. Промежуточных в наблюдениях не было.
MAX_IDLE = 0.05
ATTEMPTS = 4


def run_load(cmd, cwd, env):
    """Возвращает (data | None, discarded) — разбор последней строки stdout."""
    discarded = []
    for attempt in range(1, ATTEMPTS + 1):
        p = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True,
                           encoding="utf-8", env=env)
        lines = (p.stdout or "").strip().splitlines()
        if not lines:
            return None, discarded
        data = json.loads(lines[-1])
        # Ноль запросов — это отказ, а не результат «нулевая скорость».
        # Разбор такой строки прошёл бы: rps=0, простой 0, ошибок нет по
        # виду. Один такой прогон уже выглядел как удачный.
        if not data.get("count"):
            return None, discarded
        idle = data.get("idle_fraction", 0.0)
        if idle <= MAX_IDLE or attempt == ATTEMPTS:
            data["idle_discarded"] = discarded
            # Если исчерпаны попытки, а простой всё ещё велик — это видно
            # в самой строке, и анализатор обязан такую клетку отвергнуть.
            return data, discarded
        discarded.append(round(idle, 4))
    return None, discarded
