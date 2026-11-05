# -*- coding: utf-8 -*-
"""Сводки двух сессий сверяются величинами — и различие сессий доказано.

Здесь три требования, и третье важнее первых двух:

1. Опубликованные величины двух сводок сходятся.
2. Каждое поле фикстуры либо публикуется, либо исключено с причиной —
   иначе величина выпадает из сводки молча, и сверка её не хватится.
3. ПОЛЕ, ИСКЛЮЧЁННОЕ ИЗ СРАВНЕНИЯ, РАЗЛИЧАЕТСЯ. Свидетель — сумма
   длительностей ответов оси пула. Совпал — сводки сняты одним прогоном,
   и «числа держатся между сессиями» ложно.
"""

import importlib.util
import io
import json
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURES = os.path.join(HERE, "fixtures")
MANIFESTS = os.path.join(FIXTURES, "manifests")

PREVIOUS = "commit-92c3b1d"
CURRENT = "current"


# РАСХОЖДЕНИЯ, НАЗВАННЫЕ ПОИМЁННО.
#
# Не «поле целиком не сравниваем»: у beast и crow оно продолжает
# сверяться. Список обязан СРАБАТЫВАТЬ — протухшая запись роняет
# проверку так же, как незаписанное расхождение.
KNOWN = {
    "pool/drogon/pool/pool=1: inflight_peak":
        "у drogon число запросов в полёте плавает между сессиями — то же "
        "свойство, что и неустойчивая ёмкость на оси 1 и неустойчивое "
        "число соединений при пуле 8 (см. README). Флага устойчивости у "
        "этого поля фикстура не несёт, поэтому расхождение названо здесь, "
        "а не спрятано исключением поля",
}


def _module():
    path = os.path.join(HERE, "scripts", "run-manifest.py")
    spec = importlib.util.spec_from_file_location("run_manifest_module", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _known_of(problem):
    return problem.split(" — ", 1)[0]


class ManifestsAgree(unittest.TestCase):

    def setUp(self):
        self.module = _module()
        for name in (PREVIOUS, CURRENT):
            path = os.path.join(MANIFESTS, name + ".json")
            self.assertTrue(os.path.exists(path),
                            "нет сводки %s: сравнивать нечего" % path)
        self.left = self.module.load(PREVIOUS)
        self.right = self.module.load(CURRENT)
        self.problems, self.notes = self.module.compare(self.left, self.right)

    def test_published_values_agree(self):
        unexpected = [item for item in self.problems
                      if _known_of(item) not in KNOWN]
        self.assertEqual(unexpected, [],
                         "величины двух сессий разошлись, и расхождение "
                         "нигде не названо")

    def test_every_known_divergence_still_fires(self):
        """Список известных расхождений не имеет права протухать."""
        fired = {_known_of(item) for item in self.problems}
        stale = sorted(set(KNOWN) - fired)
        self.assertEqual(stale, [],
                         "запись о расхождении больше не срабатывает: "
                         "величина сошлась, а оговорка осталась")

    def test_the_witness_differs(self):
        """Исключённое поле обязано РАЗЛИЧАТЬСЯ между сводками."""
        field = "!" + self.module.WITNESS["pool"][0]
        left = self.left["axes"]["pool"]
        right = self.right["axes"]["pool"]
        shared = sorted(set(left) & set(right))
        self.assertTrue(shared, "у сводок нет ни одного общего плеча оси пула")
        seen = 0
        for key in shared:
            if field in left[key] and field in right[key]:
                seen += 1
                self.assertNotEqual(
                    left[key][field], right[key][field],
                    "свидетель %s совпал у плеча %s: сводки сняты ОДНИМ "
                    "прогоном, и вывод о двух сессиях ложен" % (field, key))
        self.assertTrue(seen, "свидетеля нет ни в одном общем плече")

    def test_the_witness_is_not_compared_as_a_value(self):
        """Свидетель не попадает в сверку величин.

        Иначе он краснил бы ВСЕГДА — он для того и выбран, что
        различается по построению, — и сверка величин утонула бы в одном
        заведомо красном поле.
        """
        field = "!" + self.module.WITNESS["pool"][0]
        problems, _ = self.module._compare_arm(
            "pool", "проба", {field: 1, "served": 24}, {field: 2,
                                                        "served": 24})
        self.assertEqual(problems, [],
                         "свидетель сравнивается как величина")

    def test_a_coinciding_witness_is_caught(self):
        """Диверсия на месте: подкрутим свидетеля так, чтобы он совпал.

        Без этой пробы «свидетель различается» держалось бы на том, что
        он различается САМ, а не на том, что различие проверяется.
        """
        left = json.loads(json.dumps(self.left))
        field = "!" + self.module.WITNESS["pool"][0]
        for key, values in left["axes"]["pool"].items():
            if field in values and field in self.right["axes"]["pool"][key]:
                values[field] = self.right["axes"]["pool"][key][field]
        problems, _ = self.module.compare(left, self.right)
        self.assertTrue(any("СВИДЕТЕЛЬ" in item for item in problems),
                        "совпавший свидетель не пойман")


class EveryFieldIsClassified(unittest.TestCase):
    """Поле фикстуры, не попавшее ни в публикацию, ни в исключения."""

    def test_current_tree_builds_strictly(self):
        module = _module()
        manifest = module.build(module._from_tree, "проба", "рабочее дерево")
        self.assertEqual(sorted(manifest["axes"]), sorted(module.AXIS_FILES),
                         "не все оси попали в сводку рабочего дерева")
        self.assertEqual(manifest["unknown_fields"], {})

    def test_an_unclassified_field_stops_the_manifest(self):
        module = _module()
        rows = module.rows_of(
            io.open(os.path.join(FIXTURES, "cancel.jsonl"),
                    encoding="utf-8").read(), "cancel.jsonl")
        rows[0]["neizvestnoe_pole"] = 1
        with self.assertRaises(SystemExit):
            module.axis_values("cancel", rows)

    def test_every_excluded_field_carries_a_reason(self):
        module = _module()
        for axis, fields in sorted(module.EXCLUDED.items()):
            for name, reason in sorted(fields.items()):
                self.assertTrue(reason and len(reason) > 10,
                                "поле %s оси %s исключено без причины"
                                % (name, axis))

    def test_an_unfinished_fixture_is_refused(self):
        """Оборванная фикстура даёт МЕНЬШЕ строк, а не ошибку."""
        module = _module()
        with self.assertRaises(SystemExit):
            module.rows_of('{"kind":"cancel","variant":"beast"}\n', "проба")
