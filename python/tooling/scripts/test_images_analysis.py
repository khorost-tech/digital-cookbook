"""Предохранители разбора образов.

Главный из них — «часть не больше целого». Он появился не из
осторожности: первая версия сценария писала в одну строку размер образа
из docker image inspect и размер самого толстого слоя из docker history,
не заметив, что при containerd-хранилище это числа из разных семей —
сжатой и распакованной. В таблице получилось, что отдельный слой весит
87 МБ при образе в 61 МБ. Ни одна проверка тогда не сработала, потому что
проверки на это не было.

Остальные — про то же самое, что и в сценарии установки: сравнение имеет
смысл, только пока плечи делают одинаковую работу.
"""

import importlib.util
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location(
    "analyze_images", ROOT / "scripts" / "analyze-images.py")
an = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(an)


def run_row(strategy, round_no, boot, digest="abc123", unpacked=100_000_000,
            fattest=40_000_000, reported=None):
    compressed = unpacked // 3
    if reported is None:
        reported = compressed + unpacked
    return {
        "kind": "run", "strategy": strategy, "round": round_no,
        "boot_seconds": boot, "answer_sha256": digest, "answer_bytes": 497,
        "compressed_bytes": compressed, "unpacked_bytes": unpacked,
        "reported_bytes": reported, "reported_size": f"{reported // 10**6}MB",
        "fattest_layer_bytes": fattest, "layers": 7, "history_steps": 12,
    }


def rebuild_row(names, unpacked=100_000_000, drift=0):
    return {"kind": "rebuild", "checks": {
        n: {"layers_equal": True,
            "compressed_before": unpacked // 3,
            "compressed_after": unpacked // 3 + 400,
            "unpacked_before": unpacked,
            "unpacked_after": unpacked + drift}
        for n in names}}


def statics_of(runs):
    """Ровно то же, что делает разбор, — включая поля третьего размера.

    Первая версия этой заготовки их не копировала, и проверка тождества
    молча не срабатывала: тест на испорченном тождестве проходил, потому
    что вердикту нечего было проверять.
    """
    out = {}
    for r in runs:
        out.setdefault(r["strategy"], {
            k: r[k] for k in ("compressed_bytes", "unpacked_bytes",
                              "fattest_layer_bytes", "layers",
                              "reported_size", "reported_bytes")
            if k in r})
    return out


class Verdict(unittest.TestCase):

    def check(self, runs, rebuild):
        return an.verdict(runs, rebuild, statics_of(runs))

    def test_clean_data_passes(self):
        runs = [run_row("slim", 1, 0.6), run_row("uv", 1, 1.9)]
        self.assertEqual(self.check(runs, rebuild_row(["slim", "uv"])), [])

    def test_different_answers_are_fatal(self):
        """Образы обязаны отвечать одинаково, иначе размеры не о чём."""
        runs = [run_row("slim", 1, 0.6),
                run_row("uv", 1, 1.9, digest="deadbeef")]
        problems = self.check(runs, rebuild_row(["slim", "uv"]))
        self.assertTrue(any("РАЗНОЕ" in p for p in problems), problems)

    def test_layer_bigger_than_image_is_fatal(self):
        """Ровно тот дефект, ради которого проверка и написана."""
        runs = [run_row("multistage", 1, 1.0, unpacked=61_127_037,
                        fattest=87_425_024)]
        problems = self.check(runs, rebuild_row(["multistage"]))
        self.assertTrue(any("разных семей" in p for p in problems), problems)

    def test_unstable_rebuild_is_fatal(self):
        """Распакованный размер обязан повторяться побайтово."""
        runs = [run_row("slim", 1, 0.6)]
        problems = self.check(runs, rebuild_row(["slim"], drift=4096))
        self.assertTrue(any("не воспроизводятся" in p for p in problems),
                        problems)

    def test_compressed_drift_alone_is_not_a_defect(self):
        """Сжатый размер плавает от сборки к сборке — и это норма.

        Заготовка rebuild_row всегда добавляет 400 байт к сжатому
        размеру. Если бы вердикт судил по нему, не проходил бы НИ ОДИН
        замер: gzip не обязан выдавать побайтово одинаковый поток.
        Сравнение стратегий идёт по распакованному размеру, где разница
        между ближайшими плечами — мегабайты против сотен байт дрейфа.
        """
        runs = [run_row("slim", 1, 0.6)]
        self.assertEqual(self.check(runs, rebuild_row(["slim"])), [])

    def test_missing_rebuild_check_is_fatal(self):
        """Молчание о воспроизводимости — не то же самое, что её наличие."""
        runs = [run_row("slim", 1, 0.6)]
        problems = self.check(runs, None)
        self.assertTrue(any("не подтверждена" in p for p in problems),
                        problems)


class Paired(unittest.TestCase):

    def test_ratio_is_taken_inside_a_round(self):
        """Круг 1 медленный целиком, круг 2 быстрый — отношение то же.

        Числа подобраны так, чтобы счёт «медиана на медиану» ошибся:
        медианы дали бы 2,25x, покруговый счёт даёт ровно 2x в обоих
        кругах.
        """
        runs = [run_row("slim", 1, 1.0), run_row("uv", 1, 2.0),
                run_row("slim", 2, 0.5), run_row("uv", 2, 1.0)]
        self.assertEqual(an.paired(runs, "uv"), [2.0, 2.0])

    def test_round_without_a_pair_is_dropped(self):
        runs = [run_row("slim", 1, 1.0), run_row("uv", 1, 2.0),
                run_row("uv", 2, 1.0)]
        self.assertEqual(an.paired(runs, "uv"), [2.0])


class RealFixture(unittest.TestCase):

    def setUp(self):
        self.fixture = ROOT / "fixtures" / "images.txt"
        if not self.fixture.exists():
            self.skipTest("фикстуры нет")
        an.FIXTURE = self.fixture
        self.data = an.rows()
        self.runs = [r for r in self.data if r.get("kind") == "run"]
        self.rebuild = next(
            (r for r in self.data if r.get("kind") == "rebuild"), None)

    def test_fixture_passes(self):
        self.assertEqual(
            an.verdict(self.runs, self.rebuild, statics_of(self.runs)), [])
        self.assertEqual(len({r["strategy"] for r in self.runs}),
                         len(an.ORDER))

    def test_fixture_survives_only_because_it_is_clean(self):
        """Порча одной строки обязана менять вердикт.

        Проверка «фикстура годна» ничего не стоит, если она проходит и на
        негодной.
        """
        spoiled = [dict(r) for r in self.runs]
        spoiled[0] = dict(spoiled[0], answer_sha256="0" * 16)
        self.assertNotEqual(
            an.verdict(spoiled, self.rebuild, statics_of(spoiled)), [])

    def test_bytecode_probe_is_recorded_for_every_shell_image(self):
        """Проба обязана быть у всех, кроме distroless.

        Пропущенная проба выглядит в таблице прочерком, а прочерк легко
        прочитать как «дописывания нет». Это разные утверждения.
        """
        env = next(r for r in self.data if r.get("kind") == "env")
        for name in an.ORDER:
            probe = env["bytecode"].get(name)
            if name == "distroless":
                self.assertIsNone(probe)
                continue
            self.assertIsNotNone(probe, name)
            self.assertIn("pyc_rewritten", probe)


class ReportedSize(unittest.TestCase):
    """Третье число Docker: docker image ls = сжатое + распакованное."""

    def test_identity_holds(self):
        runs = [run_row("slim", 1, 0.6)]
        self.assertEqual(
            an.verdict(runs, rebuild_row(["slim"]), statics_of(runs)), [])

    def test_rounding_is_tolerated(self):
        """Docker печатает округлённо, и требовать точности нельзя.

        360,4 МБ он покажет как «360MB». Проверка с нулевым допуском
        забраковала бы каждый исправный замер.
        """
        runs = [run_row("slim", 1, 0.6, reported=133_000_000)]
        self.assertEqual(
            an.verdict(runs, rebuild_row(["slim"]), statics_of(runs)), [])

    def test_broken_identity_is_fatal(self):
        """Если третье число не сходится, подпись под колонками неверна.

        Это и есть та проверка, которой в первой версии не было: равенство
        трёх размеров стояло в тексте как утверждение автора, а в фикстуре
        лежали только два числа из трёх.
        """
        runs = [run_row("slim", 1, 0.6, reported=200_000_000)]
        problems = an.verdict(runs, rebuild_row(["slim"]), statics_of(runs))
        self.assertTrue(any("тождество не выполняется" in p
                            for p in problems), problems)


def env_row(digests=None, drifted=None):
    if digests is None:
        digests = {"python:3.14-slim": {"index": "sha256:" + "a" * 8,
                                        "platform": "sha256:" + "b" * 8,
                                        "target_platform": "linux/amd64"}}
    return {"kind": "env", "base_digests": digests,
            "base_digests_drifted": drifted or []}


class BaseDigests(unittest.TestCase):

    def test_full_digests_pass(self):
        runs = [run_row("slim", 1, 0.6)]
        self.assertEqual(
            an.verdict(runs, rebuild_row(["slim"]), statics_of(runs),
                       env_row()), [])

    def test_index_without_platform_is_fatal(self):
        """Digest индекса — ссылка на НАБОР манифестов, а не на собранное.

        Записать только его значит записать «один из нескольких образов»,
        и понять по такой записи, что легло в слои, нельзя.
        """
        runs = [run_row("slim", 1, 0.6)]
        env = env_row({"python:3.14-slim": {"index": "sha256:" + "a" * 8,
                                            "platform": None,
                                            "target_platform": "linux/amd64"}})
        problems = an.verdict(runs, rebuild_row(["slim"]), statics_of(runs),
                              env)
        self.assertTrue(any("не полностью" in p for p in problems), problems)

    def test_missing_digests_are_fatal(self):
        runs = [run_row("slim", 1, 0.6)]
        problems = an.verdict(runs, rebuild_row(["slim"]), statics_of(runs),
                              env_row(digests={}))
        self.assertTrue(any("не записан" in p for p in problems), problems)

    def test_registry_drift_during_the_run_is_fatal(self):
        """Если реестр сменил базу посреди прогона, образы несравнимы."""
        runs = [run_row("slim", 1, 0.6)]
        problems = an.verdict(runs, rebuild_row(["slim"]), statics_of(runs),
                              env_row(drifted=["python:3.14-slim"]))
        self.assertTrue(any("сменил базовый образ" in p for p in problems),
                        problems)

    def test_local_id_does_not_identify_the_platform(self):
        """Локальный Id совпадает с digest ИНДЕКСА, а не манифеста.

        Это не догадка, а свойство containerd-хранилища, проверенное на
        всех базах стенда. Тест держит его записанным: без него легко
        решить, что локальных полей достаточно, и перестать снимать
        digest платформы из реестра — а тогда в фикстуре останется
        ссылка на набор образов вместо ссылки на собранный.
        """
        fixture = ROOT / "fixtures" / "images.txt"
        if not fixture.exists():
            self.skipTest("фикстуры нет")
        an.FIXTURE = fixture
        env = next(r for r in an.rows() if r.get("kind") == "env")
        for ref, info in env["base_digests"].items():
            self.assertEqual(info["image_id"], info["index"], ref)
            self.assertNotEqual(info["image_id"], info["platform"], ref)

    def test_local_copies_did_not_change_during_the_run(self):
        """Локальная база не подменялась посреди прогона."""
        fixture = ROOT / "fixtures" / "images.txt"
        if not fixture.exists():
            self.skipTest("фикстуры нет")
        an.FIXTURE = fixture
        env = next(r for r in an.rows() if r.get("kind") == "env")
        self.assertEqual(env.get("base_digests_drifted"), [])

    def test_every_base_image_has_both_digests(self):
        """В настоящей фикстуре у каждой базы записаны оба digest."""
        fixture = ROOT / "fixtures" / "images.txt"
        if not fixture.exists():
            self.skipTest("фикстуры нет")
        an.FIXTURE = fixture
        env = next(r for r in an.rows() if r.get("kind") == "env")
        digests = env.get("base_digests")
        self.assertTrue(digests, "digest базовых образов не записаны")
        for ref, info in digests.items():
            self.assertTrue((info or {}).get("index", "").startswith("sha256:"),
                            f"{ref}: индекс {info}")
            self.assertTrue((info or {}).get("platform", "").startswith(
                "sha256:"), f"{ref}: платформа {info}")

    def test_store_driver_is_recorded(self):
        fixture = ROOT / "fixtures" / "images.txt"
        if not fixture.exists():
            self.skipTest("фикстуры нет")
        an.FIXTURE = fixture
        env = next(r for r in an.rows() if r.get("kind") == "env")
        self.assertIn("driver_status", env["store"])
        self.assertTrue(env["store"]["driver_status"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
