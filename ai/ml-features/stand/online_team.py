"""Онлайн-путь: сервис скоринга считает те же признаки сам, по потоку платежей.

Его писала другая команда по той же спецификации — «число платежей за 24 часа»,
«средняя сумма за 30 дней», «минут с прошлого платежа». Три места, где
прочтение разошлось с батчем, — каждое правдоподобно само по себе:

- current  — платёж кладётся в состояние до расчёта, и окно включает его самого;
- calendar — «за 30 дней» понято как 30 календарных дней по дате UTC;
- default  — пропуск заменяется нулём (среднее, минуты), отношение — единицей.

FIXES выключает расхождения по одному — для атрибуции.
"""

import datetime as dt
from collections import defaultdict, deque

import numpy as np

DAY = 86400
SOURCES = ("current", "calendar", "default")


def midnight(ts):
    d = dt.datetime.fromtimestamp(ts, dt.timezone.utc).date()
    return int(dt.datetime(d.year, d.month, d.day, tzinfo=dt.timezone.utc).timestamp())


class OnlineFeatures:
    def __init__(self, fixes=()):
        self.fixes = set(fixes)
        self.events = defaultdict(deque)   # пользователь → (время, сумма) за последние 31 день
        self.last = {}

    def observe(self, user, ts, amount):
        q = self.events[user]
        q.append((ts, amount))
        while q and q[0][0] < ts - 31 * DAY:
            q.popleft()
        self.last[user] = ts

    def request(self, user, ts, amount):
        prev = self.last.get(user)               # минуты с прошлого платежа команда считала верно
        if "current" not in self.fixes:
            self.observe(user, ts, amount)       # сначала записали событие, потом посчитали
        q = self.events[user]
        if "current" in self.fixes:
            in24 = [a for t, a in q if ts - DAY <= t < ts]
        else:
            in24 = [a for t, a in q if ts - DAY < t <= ts]
        lo30 = (ts - 30 * DAY) if "calendar" in self.fixes else (midnight(ts) - 30 * DAY)
        in30 = [a for t, a in q if lo30 <= t and (t < ts if "current" in self.fixes else t <= ts)]
        avg = (sum(in30) / len(in30)) if in30 else np.nan
        minutes = (ts - prev) / 60.0 if prev is not None else np.nan
        ratio = amount / avg if avg == avg else np.nan
        if "default" not in self.fixes:
            avg = 0.0 if avg != avg else avg
            minutes = 0.0 if minutes != minutes else minutes
            ratio = 1.0 if ratio != ratio else ratio
        if "current" in self.fixes:
            self.observe(user, ts, amount)
        return {"tx_count_24h": float(len(in24)), "amount_avg_30d": avg, "minutes_since_prev": minutes, "amount_ratio": ratio}
