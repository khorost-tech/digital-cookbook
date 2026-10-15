"""Нагрузчик: одна цель, один эндпоинт, один круг.

Печатает в stdout ровно одну строку JSON. Всё остальное идёт в stderr.

ПОЧЕМУ ТАК УСТРОЕНО. Первая версия была однопроцессной на httpx, и
scripts/check-loader.sh её отверг: один процесс упирался в 251 запрос в
секунду. Многопроцессная на httpx тоже не прошла — пропускная способность
росла с числом процессов и не выходила на полку даже на четырнадцати ядрах.
То есть замер мерил бы клиента, а все фреймворки показали бы примерно
одинаковые числа: вывод «фреймворки не различаются» звучит правдоподобно и
потому особенно опасен.

Поэтому здесь узкий клиент на голом asyncio (httpfast.py) И несколько
процессов. Порог проверки при этом не трогался: подогнать проверку под
слабый нагрузчик было бы подменой.

Поэтому нагрузка раскладывается по нескольким процессам, а родитель сводит
их результаты. Задержки берутся из объединённой выборки всех процессов.

Пропускная способность считается как СУММА ПОПРОЦЕССНЫХ скоростей:
каждый ребёнок сообщает свои count и wall, родитель складывает count_i/wall_i.

Так было не сразу. Первая версия мерила время родителя вокруг детей целиком
и при одновременности 1 выдавала 5.89 запросов в секунду вместо примерно 45:
в окно попадал прогрев. Вторая брала объединённое окно от самого раннего
начала до самого позднего конца — и оказалась хуже, чем выглядела.

Объединённое окно чувствительно к РАЗЛЁТУ СТАРТОВ. Если один процесс
запоздал, в окно попадает период, когда работали не все, и пропускная
способность проваливается, хотя цель ни при чём. На матрице это дало
двумодальное распределение: восемь кругов одной клетки разложились на 1857,
1857, 1871 и 2763, 2783, 2795, 2799, 2826 — при ОДИНАКОВОМ числе запросов и
ОДИНАКОВОЙ задержке 22.5 мс. Разница была только в длине окна: 6.88 с
против 4.58 с, то есть фактическая одновременность 41.7 вместо 63.2.

Сумма попроцессных скоростей от разлёта стартов не зависит: каждый ребёнок
измеряет только своё время и своё число запросов.

Но и после этого примерно один круг из десяти проваливался вдвое. Разбор
показал, что проваливается не цель: в таком круге ЗАНЯТОЕ время нормальное
(3.37 с против обычных 3.4), задержки нормальные вплоть до p99, а окно
растянуто до 5.76 с. То есть 2.4 секунды нагрузчик не ждал НИ ОДНОГО
ответа — он просто стоял.

Цель так испортить замер не может: её замедление попало бы в длительности
запросов, а они не изменились. Поэтому такой круг мерил заминку клиента, и
его нельзя ни усреднять с остальными, ни выкидывать молча.

БЮДЖЕТ ВРЕМЕНИ. Число запросов сценарий считает от ТЕОРЕТИЧЕСКОГО потолка
«одновременность / задержка». Плечи, которые до этого потолка не достают,
на нём и застревают: синхронный WSGI держит свои 45 запросов в секунду при
любой одновременности, и клетка, задуманная на четыре секунды, тянулась 284.
Два плеча из пяти съедали 90% круга — прогон упирался ровно в тот результат,
который измеряет.

Поэтому есть --max-seconds. Работает он НЕ обрывом по таймеру: обрыв бросает
запросы в полёте, окно считается целиком, и на медленном плече вместо
устойчивых 44.9 выходили 35.8 и 49.0. Вместо этого короткая проба измеряет
скорость плеча, по ней берётся число запросов на нужную длительность, и
дальше идёт ПОЛНАЯ пачка — окно чистое, ничего не брошено. Парная к нему
--min-seconds задаёт нижнюю границу: пачка короче неё мерит не цель, а шум
планировщика (на /ping при c=64 окно в треть секунды давало разброс 1.61x
против 1.04–1.09x у клеток длиннее полусекунды). Уменьшенное
против плана число запросов всё равно помечается: count_planned и truncated.

Отсюда поле idle_fraction: доля времени, которую соединения простояли между
запросами. Считается по ПАУЗАМ ВНУТРИ каждого соединения — от конца одного
запроса до начала следующего, — а не как «окно минус занятость».

Разница существенная. Суммарная версия считала простоем нормальный хвост
слива: в конце пачки очередь пустеет, соединения заканчивают вразнобой, и
на клетке c=64 при запросе в 1.3 секунды это давало ровные 7–10% на всех
кругах подряд. Отбраковка срабатывала 22 раза там, где ничего не случилось.
Паузы внутри соединения хвоста не видят: после последнего запроса пауз нет.
Сценарии обязаны отбрасывать круги выше порога и ЗАПИСЫВАТЬ, сколько
отброшено, — иначе отбор превратится в подгонку.

Три свойства, без которых замер не имел бы смысла:

* прогон с ненулевым числом ошибок результатом НЕ является — поле errors
  выносится наружу, и разбор обязан такой прогон отвергнуть;
* хеш тела ответа кладётся в результат: если под нагрузкой контракт поехал,
  это будет видно прямо в фикстуре;
* прогрев идёт вне измеряемого интервала.
"""

import argparse
import asyncio
import os
import hashlib
import json
import subprocess
import sys
import time
import urllib.parse

from httpfast import Connection
from measure import percentile

SAMPLE = {"id": 42, "name": "тестовое имя", "tags": ["a", "b", "c"]}

# Потолок очереди для прогрева и пробы: столько запросов не сделает за
# отведённое время ни одно плечо, поэтому ограничителем остаётся время.
WARM_CEILING = 300000
WARMUP_SECONDS = 1.0


async def worker(conn_host, conn_port, method, path, body, queue, out,
                 deadline=None):
    """Одно соединение обслуживает запросы из общей очереди."""
    conn = Connection(conn_host, conn_port)
    try:
        while True:
            if deadline is not None and time.perf_counter() >= deadline:
                return
            try:
                queue.get_nowait()
            except asyncio.QueueEmpty:
                return
            t0 = time.perf_counter()
            try:
                status, payload = await conn.request(method, path, body)
                ok = status == 200
            except Exception:
                ok, payload = False, b""
                await conn.close()
            out.append((t0, time.perf_counter() - t0, ok, payload))
    finally:
        await conn.close()


def idle_gaps(lanes):
    """Паузы внутри соединений и суммарный размах их работы.

    Пауза — время от конца одного запроса до начала следующего В ТОМ ЖЕ
    соединении. Хвост слива паузой не является: после последнего запроса
    следующего нет, и его отсутствие ни о чём не говорит.
    """
    gaps = spans = 0.0
    for lane in lanes:
        if len(lane) < 2:
            continue
        lane = sorted(lane, key=lambda r: r[0])
        spans += (lane[-1][0] + lane[-1][1]) - lane[0][0]
        for prev, nxt in zip(lane, lane[1:]):
            gaps += max(0.0, nxt[0] - (prev[0] + prev[1]))
    return gaps, spans


async def run_child(url, payload, concurrency, count, warmup, budget,
                    floor_seconds=0.0):
    parsed = urllib.parse.urlparse(url)
    host, port = parsed.hostname, parsed.port or 80
    method = "GET" if payload is None else "POST"
    body = None if payload is None else json.dumps(payload).encode("utf-8")

    async def burst(n, seconds):
        queue = asyncio.Queue()
        for _ in range(n):
            queue.put_nowait(1)
        # Каждое соединение пишет в СВОЙ список. Общий список не даёт
        # отличить разовую заминку от нормального хвоста слива: в конце
        # пачки очередь пустеет, и соединения заканчивают вразнобой.
        lanes = [[] for _ in range(concurrency)]
        deadline = time.perf_counter() + seconds if seconds else None
        await asyncio.gather(*[
            worker(host, port, method, parsed.path, body, queue, lane, deadline)
            for lane in lanes])
        return lanes

    # Прогрев вне измеряемого интервала. Ограничен и по числу запросов, и
    # по времени, причём число берётся с запасом: на быстром плече заданные
    # 256 запросов заканчиваются за пять сотых секунды и цель прогреть не
    # успевают. Ограничителем должно быть ВРЕМЯ, а не счётчик.
    if budget:
        await burst(max(warmup, WARM_CEILING), WARMUP_SECONDS)
    else:
        await burst(warmup, None)

    # Размер измеряемой пачки — по ПРОБЕ, а не обрывом по таймеру.
    # Обрыв на лету пробовали: он бросает запросы в полёте, окно при этом
    # считается целиком, и на медленном плече вместо устойчивых 44.9 выходили
    # 35.8 и 49.0. Поэтому: короткая проба даёт скорость, по скорости берётся
    # число запросов на нужную длительность, и дальше идёт ПОЛНАЯ пачка.
    if budget:
        # Проба тоже не должна упираться в плановое число запросов: иначе
        # она измеряет скорость на слишком коротком интервале и занижает её,
        # а по заниженной скорости измеряемая пачка выходит короче нижней
        # границы. Ограничитель у пробы — только время.
        probe_seconds = max(1.0, min(3.0, budget / 4))
        probe = [r for lane in await burst(max(count, WARM_CEILING),
                                           probe_seconds) for r in lane]
        if probe:
            elapsed = max(r[0] + r[1] for r in probe) - min(r[0] for r in probe)
            rate = len(probe) / elapsed if elapsed > 0 else 0.0
            if rate > 0:
                # Пачка обязана длиться не меньше floor_seconds и не больше
                # budget. Нижняя граница появилась не сразу: на быстрых
                # плечах двух тысяч запросов хватало на треть секунды, и
                # разброс по кругам доходил до 1.61x при 1.04–1.09x у клеток
                # с окном длиннее полусекунды. Разброс был свойством
                # длительности замера, а не цели.
                lo = int(rate * floor_seconds)
                hi = int(rate * budget)
                count = max(concurrency * 4, min(hi, max(lo, count)))

    # Окно измерения — РАЗМАХ САМИХ ЗАПРОСОВ, от начала первого до конца
    # последнего, а не время вокруг пачки. Разница не косметическая: между
    # взятием отметки и первым запросом есть мёртвое время (открытие
    # соединений, раскрутка пула воркеров), и на медленном плече оно давало
    # постоянные три секунды. В короткой пачке это 17% окна, в длинной 8% —
    # то есть измеренная скорость зависела бы от длины пачки, а не от цели.
    offset = time.time() - time.perf_counter()
    lanes = await burst(count, None)
    results = [r for lane in lanes for r in lane]
    if not results:
        now = time.time()
        return results, now, now, 0.0, 0.0
    started = min(r[0] for r in results) + offset
    ended = max(r[0] + r[1] for r in results) + offset

    # Простой считается по ПАУЗАМ ВНУТРИ соединения: от конца одного запроса
    # до начала следующего. Так хвост слива не считается простоем — после
    # последнего запроса пауз уже нет, — а заминка считается, потому что она
    # приходится на середину и разрывает цепочку.
    gaps, spans = idle_gaps(lanes)
    return results, started, ended, gaps, spans


def child_main(args):
    # Ядра нагрузчика задаются извне: цель живёт на своих, и они не
    # пересекаются. Иначе нагрузчик отбирает процессор у цели, и её
    # пропускная способность падает тем сильнее, чем сильнее обстрел.
    spec = os.environ.get("LOADER_CPUS")
    if spec:
        from pin import parse_cpus
        os.sched_setaffinity(0, parse_cpus(spec))
    payload = None if args.endpoint == "ping" else SAMPLE
    results, started, ended, gaps, spans = asyncio.run(
        run_child(args.url, payload, args.concurrency, args.count,
                  args.warmup, args.max_seconds, args.min_seconds))
    body = next((r[3] for r in reversed(results) if r[2]), b"")
    payload = {
        "started": started,
        "ended": ended,
        "durations": [round(r[1], 6) for r in results],
        "errors": sum(1 for r in results if not r[2]),
        "gap_s": gaps,
        "span_s": spans,
        "body_sha256": hashlib.sha256(body).hexdigest()[:16],
    }
    # Пофразовые отметки нужны только сценарию завершения: там важно, какие
    # запросы БЫЛИ В ПОЛЁТЕ в момент сигнала. Остальным фикстурам они лишний
    # объём, поэтому включаются флагом.
    if os.environ.get("EMIT_SAMPLES"):
        offset = time.time() - time.perf_counter()
        payload["samples"] = [[round(r[0] + offset, 6), round(r[1], 6),
                               1 if r[2] else 0] for r in results]
    print(json.dumps(payload))
    return 0


def parent_main(args):
    # Число процессов не может превышать заданную одновременность: иначе
    # каждый ребёнок получил бы concurrency=1, и ФАКТИЧЕСКАЯ одновременность
    # оказалась бы равна числу процессов. На этом замер уже попался: при
    # --concurrency 1 --procs 4 плечо /wait выдавало 136 запросов в секунду
    # при потолке 50, потому что одновременность была не 1, а 4.
    n_procs = min(args.procs, args.concurrency)
    per_proc = max(1, args.count // n_procs)
    conc_per_proc = args.concurrency // n_procs
    warmup_per_proc = max(1, args.warmup // n_procs)

    cmd = [sys.executable, __file__, "--child",
           "--url", args.url, "--framework", args.framework,
           "--endpoint", args.endpoint,
           "--concurrency", str(conc_per_proc),
           "--count", str(per_proc),
           "--warmup", str(warmup_per_proc),
           "--max-seconds", str(args.max_seconds),
           "--min-seconds", str(args.min_seconds)]

    procs = [subprocess.Popen(cmd, stdout=subprocess.PIPE,
                              stderr=subprocess.DEVNULL, text=True,
                              encoding="utf-8")
             for _ in range(n_procs)]
    outs = [p.communicate()[0] for p in procs]

    durations, errors, hashes = [], 0, set()
    starts, ends, samples = [], [], []
    per_proc_rps, child_walls = [], []
    total_gaps = total_spans = 0.0
    for out in outs:
        line = out.strip().splitlines()[-1] if out.strip() else ""
        if not line:
            errors += per_proc
            continue
        data = json.loads(line)
        durations.extend(data["durations"])
        errors += data["errors"]
        hashes.add(data["body_sha256"])
        starts.append(data["started"])
        ends.append(data["ended"])
        samples.extend(data.get("samples", []))
        total_gaps += data.get("gap_s", 0.0)
        total_spans += data.get("span_s", 0.0)
        child_wall = data["ended"] - data["started"]
        child_walls.append(child_wall)
        if child_wall > 0:
            per_proc_rps.append(len(data["durations"]) / child_wall)

    total = len(durations)
    # Сумма попроцессных скоростей вместо объединённого окна: см. докстринг.
    # wall остаётся в результате как справка о длительности прогона, но
    # пропускную способность по нему НЕ считаем.
    wall = (max(ends) - min(starts)) if starts else 0.0
    rps = sum(per_proc_rps)
    # Сколько времени соединения РЕАЛЬНО ждали ответов. Если сложить все
    # длительности и разделить на число соединений, получится окно, которое
    # заняли бы те же запросы без единой паузы. Разница с фактическим окном
    # и есть простой нагрузчика.
    idle_fraction = (total_gaps / total_spans) if total_spans > 0 else 0.0
    # Разлёт стартов теперь виден явно: если он велик, объединённое окно
    # заметно длиннее самого долгого попроцессного, и это стоит заметить.
    stagger = round(wall - max(child_walls), 3) if child_walls else 0.0
    out = {
        "framework": args.framework,
        "endpoint": args.endpoint,
        "concurrency": args.concurrency,
        "procs": n_procs,
        "concurrency_per_proc": conc_per_proc,
        "count": total,
        "round": args.round_no,
        "seed": args.seed,
        "errors": errors,
        "p50_ms": round(percentile(durations, 0.50) * 1000, 3),
        "p95_ms": round(percentile(durations, 0.95) * 1000, 3),
        "p99_ms": round(percentile(durations, 0.99) * 1000, 3),
        "wall_s": round(wall, 4),
        "stagger_s": stagger,
        "gap_s": round(total_gaps, 3),
        # Доля окна, в которую нагрузчик НЕ ждал ни одного ответа. Признак
        # того, что круг мерил не цель, а заминку клиента: см. докстринг.
        "idle_fraction": round(idle_fraction, 4),
        # Сколько запросов ЗАПЛАНИРОВАНО против фактически сделанных. Если
        # бюджет времени срезал пачку, это обязано быть видно в фикстуре:
        # молча уменьшенное число запросов читается как полный прогон.
        "count_planned": per_proc * n_procs,
        "truncated": bool(total < per_proc * n_procs),
        "per_proc_rps": [round(v, 1) for v in per_proc_rps],
        "rps": round(rps, 2),
        # Если процессы увидели разные тела, контракт поехал под нагрузкой.
        "body_sha256": sorted(hashes)[0] if len(hashes) == 1 else "MISMATCH",
    }
    if samples:
        out["samples"] = samples
    print(json.dumps(out, sort_keys=True))
    return 2 if (errors or out["body_sha256"] == "MISMATCH") else 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", required=True)
    ap.add_argument("--framework", required=True)
    ap.add_argument("--endpoint", required=True,
                    choices=("ping", "echo", "wait", "cpu", "slow"))
    ap.add_argument("--concurrency", type=int, default=32)
    ap.add_argument("--count", type=int, default=4000)
    ap.add_argument("--warmup", type=int, default=400)
    ap.add_argument("--max-seconds", type=float, default=0.0,
                    help="бюджет измеряемой пачки; 0 — без ограничения")
    ap.add_argument("--min-seconds", type=float, default=0.0,
                    help="нижняя граница длительности измеряемой пачки")
    ap.add_argument("--procs", type=int, default=4,
                    help="процессов нагрузчика; один упирается в себя")
    ap.add_argument("--seed", type=int, default=0)
    ap.add_argument("--round", dest="round_no", type=int, default=1)
    ap.add_argument("--child", action="store_true",
                    help="служебный режим: один процесс нагрузки")
    args = ap.parse_args()
    return child_main(args) if args.child else parent_main(args)


if __name__ == "__main__":
    sys.exit(main())
