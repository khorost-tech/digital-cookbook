"""Сценарий образов: одно приложение, шесть стратегий сборки.

ЧТО ИЗМЕРЯЕТСЯ. Размер образа, число слоёв, размер самого толстого слоя —
всё в байтах, целыми числами, а не в округлённых «МБ» из docker history.
И отдельно — время от запуска контейнера до первого ответа.

РАЗМЕРОВ У ОБРАЗА ДВА, И ЭТО НЕ ПРИДИРКА. Docker показывает три разных
числа за одним словом «размер», и на этом стенде они разошлись втрое:

    docker image inspect .Size      113 МБ   сжатые слои: что едет по сети
    сумма docker history            247 МБ   распакованные слои: что на диске
    docker images                   360 МБ   сумма первых двух

Третье — не ошибка и не другая оценка: при containerd-хранилище узел
держит и сжатые блобы, и распакованный снимок, и docker images
складывает их.

ТРЕТЬЕ ЧИСЛО ТЕПЕРЬ ТОЖЕ ЗАПИСЫВАЕТСЯ. Первая версия сценария снимала
только два первых, а равенство «третье = сумма двух» оставалось
утверждением автора. Утверждение верное, но непроверяемое по фикстуре —
то есть ровно того сорта, которого на этом стенде быть не должно. Теперь
docker image ls опрашивается наравне с остальными, и тождество проверяет
разбор.

Сверка идёт с допуском: docker image ls печатает округлённо («360MB»), и
требовать побайтового совпадения от округлённого числа нельзя.

ЧТО ПРОВЕРЯЕТСЯ ДО ТОГО, КАК ЧИСЛА ЧТО-ТО ЗНАЧАТ. Что все шесть образов
отвечают ОДИНАКОВО. Образ легко ужать, выкинув то, без чего приложение
перестанет работать, и на весах это выглядит как победа. Поэтому у
каждого образа спрашивают /answer и сверяют хеш ответа: в ответе лежат и
результат вычисления, и версии зависимостей.

ГДЕ ЭТОТ СЦЕНАРИЙ ВЫПОЛНЯЕТСЯ. На хосте, а не внутри bench-контейнера:
ему нужен доступ к демону Docker. Остальные сценарии стенда живут в
контейнере, этот — исключение.

ГРАНИЦА ВЫВОДА ПО ВРЕМЕНИ СТАРТА. Опрос идёт с шагом POLL_S, и это пол
разрешения: разницу меньше шага опроса измерить нечем. Клетки, где время
сравнимо с шагом, ранжировать по отношению нельзя — тот же случай, что
uv/noop в сценарии установки.
"""

import hashlib
import json
import os
import socket
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import measure  # noqa: E402

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
STRATEGIES = ("naive", "slim", "multistage", "uv", "uv-compiled",
              "distroless")
TAG = "py-tooling/{name}:bench"
PORT_ENV = int(os.environ.get("PORT", "18140"))
POLL_S = 0.02
BOOT_TIMEOUT_S = 60


def run(args, **kwargs):
    done = subprocess.run(args, capture_output=True, text=True, **kwargs)
    if done.returncode != 0:
        tail = (done.stderr or done.stdout or "").strip().splitlines()
        raise RuntimeError(f"{' '.join(args)} → код {done.returncode}: "
                           + (" | ".join(tail[-4:]) or "без вывода"))
    return done.stdout


def build(name, no_cache=False):
    """Сборка БЕЗ --pull, потому что базы уже вытянуты явно.

    --no-cache и --pull решают разные задачи: первый не переиспользует
    слои сборки, второй обновляет базовый образ. Нужное здесь — второе, и
    оно сделано один раз в pull_bases: локальная копия совпадает с
    реестром, и все сборки прогона идут от неё. Ставить --pull ещё и на
    каждую сборку значит повторять ту же работу шесть десятков раз за
    прогон — на этом стенде так и приехал 429 от Docker Hub.
    """
    # --platform обязателен: базы вытянуты под PLATFORM, но без флага
    # сборка идёт под архитектуру хоста. На arm64-хосте это другой образ
    # (и distroless падает — путь /lib/x86_64-linux-gnu там не существует).
    # Но и с флагом на хосте другой архитектуры слои не распаковываются до
    # первого запуска: размеры при пересборке получаются неверными, а
    # docker image ls на Docker 29.8 считает иначе, чем на 29.7.2. Разбор
    # такой прогон бракует по тождеству; поправку к slim снимает
    # scripts/slim-ab.sh, который распаковку делает явно.
    args = ["docker", "build", "-q", "--platform", PLATFORM,
            "-f", f"images/{name}.Dockerfile", "-t", TAG.format(name=name)]
    if no_cache:
        args.append("--no-cache")
    args.append(".")
    return run(args, cwd=ROOT).strip()


def human_to_bytes(text):
    """«360MB» -> байты. Docker печатает округлённо, точности не будет."""
    units = {"B": 1, "kB": 10 ** 3, "MB": 10 ** 6, "GB": 10 ** 9,
             "KB": 10 ** 3, "K": 10 ** 3, "M": 10 ** 6, "G": 10 ** 9}
    text = text.strip()
    for suffix in sorted(units, key=len, reverse=True):
        if text.endswith(suffix):
            return int(float(text[:-len(suffix)]) * units[suffix])
    return int(float(text))


BASE_IMAGES = (
    "python:3.14",
    "python:3.14-slim",
    "debian:bookworm-slim",
    "gcr.io/distroless/base-debian12",
    "ghcr.io/astral-sh/uv:0.12.5",
)


PLATFORM = "linux/amd64"


def pull_bases():
    """Один docker pull на базу за весь прогон.

    ПОЧЕМУ НЕ --pull НА КАЖДОЙ СБОРКЕ. Так было в первой версии этого
    исправления, и она упёрлась в 429 от Docker Hub: шесть стратегий,
    каждая собирается дважды (обычно и с --no-cache), у каждой одна-две
    базы — под шесть десятков обращений к реестру за прогон. Замер
    останавливался на середине с «Too Many Requests».

    Один явный pull решает ту же задачу дешевле: после него локальная
    копия СОВПАДАЕТ с текущей в реестре, и все сборки прогона используют
    именно её. Сборкам --pull после этого не нужен.

    Платформа задаётся явно. Тег указывает на индекс манифестов под
    разные платформы; без --platform на другой машине соберётся другой
    образ, а digest в фикстуре останется прежним.
    """
    for ref in BASE_IMAGES:
        run(["docker", "pull", "--platform", PLATFORM, ref])


def local_digest(ref):
    """Чем оказалась локальная копия ПОСЛЕ pull.

    Снимается с того, что реально лежит на диске, а не с того, что реестр
    отдаёт сейчас: за время прогона реестр может обновиться. Первая версия
    спрашивала реестр ПОСЛЕ сборки, причём сборка шла без --pull, и
    записанный digest мог относиться к образу, которого сборка не видела.

    ЧЕГО ЭТИ ПОЛЯ НЕ ГОВОРЯТ. При containerd-хранилище Id локального
    образа равен digest ИНДЕКСА, а не манифеста платформы — проверено на
    всех пяти базах стенда. Значит по локальным полям видно, какой
    мультиплатформенный образ вытянут, но не какой из его манифестов
    пошёл в сборку. На это отвечает только поле platform, и оно берётся
    из реестра отдельно.
    """
    out = run(["docker", "image", "inspect", ref, "--format",
               "{{if .RepoDigests}}{{index .RepoDigests 0}}{{end}}|{{.Id}}"])
    repo_digest, _, image_id = out.strip().partition("|")
    return {
        "repo_digest": repo_digest or None,
        "image_id": image_id or None,
    }


def base_digests():
    """Что за базы участвовали в прогоне — с трёх сторон.

    repo_digest  digest, по которому база лежит локально после pull
    image_id     идентификатор самого образа на диске
    platform     digest манифеста целевой платформы из реестра

    Три поля, потому что каждое отвечает на свой вопрос и по отдельности
    ни одно не полно. Тег отдаёт digest ИНДЕКСА — списка манифестов под
    разные платформы, — а собирается ровно один из них; локальный
    image_id однозначен, но по нему нельзя вернуться в реестр.

    Digest не делает сборку воспроизводимой: для этого базы надо задавать
    как FROM ...@sha256. Он делает результат ИНТЕРПРЕТИРУЕМЫМ — читатель
    сравнит свои базы с этими и поймёт, одно ли он мерит.
    """
    out = {}
    for ref in BASE_IMAGES:
        info = dict(local_digest(ref), target_platform=PLATFORM)
        try:
            listing = run([
                "docker", "buildx", "imagetools", "inspect", ref, "--format",
                "{{range .Manifest.Manifests}}{{.Platform.OS}}/"
                "{{.Platform.Architecture}} {{.Digest}}" + chr(10) +
                "{{end}}"])
            index = run(["docker", "buildx", "imagetools", "inspect", ref,
                         "--format", "{{.Manifest.Digest}}"]).strip()
        except RuntimeError as exc:
            info.update(index=None, platform=None, error=str(exc)[:200])
            out[ref] = info
            continue
        platform = None
        for line in listing.splitlines():
            parts = line.split()
            if len(parts) == 2 and parts[0] == PLATFORM:
                platform = parts[1]
                break
        info.update(index=index or None, platform=platform)
        out[ref] = info
    return out


def image_metrics(name):
    """Размер, число слоёв и самый толстый слой — в байтах.

    --human=false существенно: без него docker history печатает «1.02GB»,
    и «воспроизводимые целые числа» превращаются в округление, где
    разница в десяток мегабайт исчезает.

    Число слоёв берётся из RootFS.Layers, то есть считает РАЗЛИЧНЫЕ слои:
    одинаковые слои у разных образов дедуплицируются, и это не искажение,
    а ровно то, что занимает место на диске.
    """
    tag = TAG.format(name=name)
    size, layers = run(
        ["docker", "image", "inspect", "-f",
         "{{.Size}} {{len .RootFS.Layers}}", tag]).split()
    sizes = [int(x) for x in run(
        ["docker", "history", "--human=false", "--format", "{{.Size}}",
         tag]).split()]
    reported = run(["docker", "image", "ls", "--format", "{{.Size}}",
                    tag]).strip().splitlines()[0]
    return {
        # Третье число Docker — то, что показывает docker image ls.
        # Округлённое, поэтому хранится и строкой, и в байтах.
        "reported_size": reported,
        "reported_bytes": human_to_bytes(reported),
        # Сжатый размер: столько весит передача из реестра.
        "compressed_bytes": int(size),
        # Распакованный: столько занимает на диске узла. Самый толстый
        # слой — из этой же семьи, иначе часть оказалась бы больше целого.
        "unpacked_bytes": sum(sizes),
        "fattest_layer_bytes": max(sizes) if sizes else 0,
        "layers": int(layers),
        "history_steps": len(sizes),
    }


def bytecode_probe(name):
    """Есть ли байт-код в образе и дописывается ли он при первом импорте.

    ЗАЧЕМ СМОТРЕТЬ НА ФАЙЛЫ, А НЕ НА ВРЕМЯ. «Первый импорт медленнее
    второго» — наблюдение, которое одинаково хорошо объясняется и
    компиляцией байт-кода, и холодным дисковым кэшем. Разделяют их только
    сами файлы .pyc.

    СЧИТАТЬ ИХ НЕДОСТАТОЧНО, и первая версия пробы именно считала.
    Устаревший байт-код Python перекомпилирует НА МЕСТЕ: файл тот же,
    число файлов не меняется, и счётчик показывает «+0» ровно там, где
    работа выполняется каждый запуск. Поэтому проба берёт время
    изменения каждого файла и различает три случая: новых не появилось,
    появилось, переписаны существующие.

    Проба уходит в фикстуру, чтобы утверждение о механизме опиралось на
    записанное число, а не на память автора.
    """
    if name == "distroless":
        # Оболочки и произвольного запуска в образе нет — на то он и
        # distroless. Подменять пробу чем-то похожим нельзя: вышло бы
        # число, снятое другим способом, в одной колонке с остальными.
        return None
    # Программа пробы собирается построчно и склеивается chr(10):
    # литеральные escape-последовательности не переживают передачу через
    # оболочку, и вместо переводов строки в контейнер уезжали два символа.
    lines = (
        "import pathlib, subprocess, sys, json",
        "sp = [p for p in map(pathlib.Path, sys.path)",
        "      if p.name == 'site-packages' and p.exists()]",
        "snap = lambda: {str(f): f.stat().st_mtime_ns",
        "                for root in sp for f in root.rglob('*.pyc')}",
        "before = snap()",
        "subprocess.run([sys.executable, '-c',",
        "                'import uvicorn, fastapi, pydantic, sqlalchemy'],",
        "               check=True)",
        "after = snap()",
        "same = set(before) & set(after)",
        "print(json.dumps({'pyc_before': len(before),",
        "                  'pyc_after': len(after),",
        "                  'pyc_new': len(set(after) - set(before)),",
        "                  'pyc_rewritten': sum(1 for k in same",
        "                                       if before[k] != after[k])}))",
    )
    code = chr(10).join(lines)
    out = run(["docker", "run", "--rm", "--entrypoint", "python",
               TAG.format(name=name), "-c", code])
    return json.loads(out.strip().splitlines()[-1])


def docker_store():
    """Хранилище образов: от него зависит, что означает .Size.

    При classic-графдрайвере inspect отдаёт распакованный размер, при
    containerd — сжатый. Без этой записи таблицу не воспроизвести: те же
    команды на другом хранилище дадут другие числа под теми же
    заголовками.
    """
    driver = run(["docker", "info", "--format", "{{.Driver}}"]).strip()
    version = run(["docker", "version", "--format",
                   "{{.Server.Version}}"]).strip()
    status = run(["docker", "info", "--format",
                  "{{json .DriverStatus}}"]).strip()
    return {"driver": driver, "server": version,
            "driver_status": json.loads(status)}


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def boot_and_ask(name):
    """Запускает контейнер, ждёт первого ответа, возвращает время и хеш.

    Время считается от возврата docker run, а не от начала команды:
    создание контейнера — работа демона, одинаковая для всех образов, и
    включать её значило бы прибавлять всем один и тот же шум.
    """
    import urllib.error
    import urllib.request

    port = free_port()
    tag = TAG.format(name=name)
    cid = run(["docker", "run", "-d", "-p", f"127.0.0.1:{port}:8000",
               tag]).strip()
    try:
        started = time.perf_counter()
        deadline = started + BOOT_TIMEOUT_S
        body = None
        while time.perf_counter() < deadline:
            try:
                with urllib.request.urlopen(
                        f"http://127.0.0.1:{port}/answer", timeout=2) as resp:
                    if resp.status == 200:
                        body = resp.read()
                        break
            except (urllib.error.URLError, ConnectionError, OSError):
                pass
            time.sleep(POLL_S)
        elapsed = time.perf_counter() - started
        if body is None:
            logs = subprocess.run(["docker", "logs", cid], capture_output=True,
                                  text=True).stderr[-400:]
            raise RuntimeError(f"{name}: ответа нет за {BOOT_TIMEOUT_S} с. "
                               f"Хвост лога: {logs}")
        return elapsed, hashlib.sha256(body).hexdigest()[:16], len(body)
    finally:
        subprocess.run(["docker", "rm", "-f", cid], capture_output=True)


def main():
    rounds = int(os.environ.get("ROUNDS", "5"))
    seed = int(os.environ.get("SEED", "0"))
    out_path = os.environ.get("OUT", os.path.join(ROOT, "fixtures",
                                                  "images.txt"))
    recheck = os.environ.get("RECHECK_BUILD", "1") == "1"

    store = docker_store()
    pull_bases()
    digests_before = base_digests()
    ids = {}
    for name in STRATEGIES:
        ids[name] = build(name)
        print(f"собран {name}", file=sys.stderr)

    # ВОСПРОИЗВОДИМОСТЬ РАЗМЕРА. Повторная сборка по кэшу вернула бы тот
    # же образ не потому, что сборка воспроизводима, а потому, что её не
    # было. Проверка чего-то стоит только с --no-cache.
    rebuilt = {}
    if recheck:
        for name in STRATEGIES:
            before = image_metrics(name)
            build(name, no_cache=True)
            after = image_metrics(name)
            rebuilt[name] = {
                "layers_equal": before["layers"] == after["layers"],
                "compressed_before": before["compressed_bytes"],
                "compressed_after": after["compressed_bytes"],
                "unpacked_before": before["unpacked_bytes"],
                "unpacked_after": after["unpacked_bytes"],
            }
            print(f"пересобран без кэша {name}: "
                  f"{before['unpacked_bytes']} → {after['unpacked_bytes']}",
                  file=sys.stderr)

    metrics = {name: image_metrics(name) for name in STRATEGIES}
    probes = {name: bytecode_probe(name) for name in STRATEGIES}
    # Сверяем ЛОКАЛЬНЫЕ копии: изменились ли они за время прогона.
    # Реестр за это время обновиться может — это его право и на собранное
    # не влияет; а вот подмена локальной базы означала бы, что часть
    # образов собрана из одной, часть из другой.
    drifted = sorted(
        ref for ref in digests_before
        if local_digest(ref)["image_id"] != digests_before[ref]["image_id"])

    with open(out_path, "a", encoding="utf-8") as out:
        out.write(json.dumps({"kind": "env", "store": store,
                              "bytecode": probes,
                              "base_digests": digests_before,
                              "base_digests_drifted": drifted},
                             ensure_ascii=False, sort_keys=True) + chr(10))
        if rebuilt:
            out.write(json.dumps({"kind": "rebuild", "checks": rebuilt},
                                 ensure_ascii=False, sort_keys=True) + "\n")
        for r in range(1, rounds + 1):
            order = measure.latin_square(seed, r, len(STRATEGIES))
            for idx in order:
                name = STRATEGIES[idx]
                seconds, digest, length = boot_and_ask(name)
                row = {
                    "kind": "run",
                    "strategy": name,
                    "round": r,
                    "boot_seconds": round(seconds, 4),
                    "answer_sha256": digest,
                    "answer_bytes": length,
                }
                row.update(metrics[name])
                out.write(json.dumps(row, ensure_ascii=False,
                                     sort_keys=True) + "\n")
                out.flush()
            print(f"круг {r} готов", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
