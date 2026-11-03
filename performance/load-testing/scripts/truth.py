"""Истина для открытой модели — расчётом, до прогона и без генератора.

Сервис svc детерминирован: C обработчиков (семафор, очередь FIFO), каждый
запрос занимает S; пауза задерживает НАЧАЛО обслуживания до конца окна
(начатое доходит до конца). Окна пауз: [k·T + (T−P), (k+1)·T) от момента
включения расписания. Для заданных моментов прихода задержка каждого запроса
считается точно — так же, как её создаёт сервис.

    python3 scripts/truth.py 200 60 1 10     → R=200/с, 60 с, P=1 с, T=10 с: p50/p99/max
"""

import heapq
import json
import math
import sys

S_MS = 10.0
C = 8


def stall_end(t, p, period, offset=0.0):
    """Если t в окне паузы — конец окна, иначе t. Время в секундах от включения расписания."""
    if not period:
        return t
    x = t - offset
    if x < 0:
        return t
    k = math.floor(x / period)
    if x - k * period >= period - p:
        return offset + (k + 1) * period
    return t


def simulate(arrivals, s=S_MS / 1000, c=C, p=0.0, period=0.0, offset=0.0):
    """Задержки (с) по моментам прихода (с, по возрастанию)."""
    free = [0.0] * c
    heapq.heapify(free)
    out = []
    for a in arrivals:
        start = max(a, heapq.heappop(free))
        start = stall_end(start, p, period, offset)
        done = start + s
        heapq.heappush(free, done)
        out.append(done - a)
    return out


def constant(rate, duration, start=0.0):
    n = int(round(rate * duration))
    return [start + i / rate for i in range(n)]


def ramp(r0, r1, duration, start=0.0):
    """Линейное изменение темпа r0 → r1 за duration: моменты по интегралу темпа."""
    out, t, n = [], 0.0, 0
    a = (r1 - r0) / duration
    while True:
        # n-й приход: r0·t + a·t²/2 = n
        if a == 0:
            t = n / r0
        else:
            disc = r0 * r0 + 2 * a * n
            if disc < 0:
                return out
            t = (-r0 + math.sqrt(disc)) / a
        if t >= duration:
            return out
        out.append(start + t)
        n += 1


def stages(start_rate, plan):
    """Кусочно-линейный темп, как ramping-arrival-rate k6: plan — [(целевой темп, длительность с)]."""
    out, t0, r0 = [], 0.0, start_rate
    for r1, d in plan:
        out += ramp(r0, r1, d, t0) if r0 or r1 else []
        t0, r0 = t0 + d, r1
    return out


def pct(xs, q):
    """Перцентиль «ближайший ранг» — как у svc (/admin/stats)."""
    s = sorted(xs)
    i = max(0, min(len(s) - 1, math.ceil(q * len(s)) - 1))
    return s[i]


def summary(lat):
    return {"requests": len(lat), "p50_ms": round(pct(lat, 0.50) * 1000, 2),
            "p90_ms": round(pct(lat, 0.90) * 1000, 2), "p99_ms": round(pct(lat, 0.99) * 1000, 2), "max_ms": round(max(lat) * 1000, 2)}


def open_model(rate, duration, p=0.0, period=0.0, offset=0.0):
    return summary(simulate(constant(rate, duration, offset), p=p, period=period, offset=0.0))


SPIKE = (400, [(400, 20), (1200, 1), (1200, 5), (400, 1), (400, 33)])
SPIKE_END = 27.0  # конец всплеска — темп вернулся к 400/с


def recovered_at(buckets, after, threshold, hold=10):
    """buckets — {номер окна 100 мс: худшая задержка}. Первое окно не раньше `after`,
    с которого задержка ≤ threshold держится hold окон подряд (1 с): одиночный
    выброс через полминуты не делает разгрузку длиннее."""
    for k in sorted(b for b in buckets if b / 10 >= after):
        if all(buckets.get(j, 0) <= threshold for j in range(k, k + hold)):
            return round(k / 10 - after, 1)
    return None


def recovery(arrivals, lat, after, s=S_MS / 1000):
    """Через сколько секунд после `after` задержка вернулась к норме (≤ 1,5·S)."""
    b = {}
    for a, d in zip(arrivals, lat):
        k = int(a * 10)
        b[k] = max(b.get(k, 0), d)
    return recovered_at(b, after, 1.5 * s)


def spike_recovery():
    arr = stages(*SPIKE)
    return recovery(arr, simulate(arr), SPIKE_END)


def knee():
    """Ёмкость C/S, обслуженных в секунду."""
    return C / (S_MS / 1000)


def main():
    if len(sys.argv) != 5:
        raise SystemExit(__doc__)
    r, d, p, t = map(float, sys.argv[1:])
    print(json.dumps(open_model(r, d, p, t), ensure_ascii=False))


if __name__ == "__main__":
    main()
