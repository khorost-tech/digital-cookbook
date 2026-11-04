"""Совпадение между платформами — либо доказано сводками, либо не заявлено.

Стенд снят на одной машине. Пока сводка одна, о совпадении величин между
платформами утверждать нечего, и проверка говорит это вслух, а не молчит.

Если сводок несколько — все опубликованные величины обязаны совпасть, а
поле, исключённое из сравнения, обязано РАЗЛИЧАТЬСЯ. Второе не менее
важно первого: свидетель платформы, совпавший у двух сводок, означает,
что прогон был один и вывод о двух платформах ложен. На стенде TLS это
исключение ловило именно такой случай.

Сравниваются величины, а не хеши файлов: фикстуры несут метки прогона,
времена сборки и времена изменения файлов и различаются при каждом
прогоне даже на одной машине.
"""

import importlib.util
import io
import json
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MANIFESTS = os.environ.get("MANIFESTS_DIR",
                           os.path.join(HERE, "fixtures", "manifests"))

# Признаки времени в имени величины. Ни одна такая величина не имеет
# права попасть в сравнение: времена зависят от хоста, и сводка,
# сравнивающая их, краснела бы на верных данных.
TIME_MARKERS = ("seconds", "_ms", "mtime", "nonce", "spread")


def run_manifest_module():
    """Сам скрипт сводки как модуль: имя с дефисом импортом не берётся."""
    path = os.path.join(HERE, "scripts", "run-manifest.py")
    spec = importlib.util.spec_from_file_location("run_manifest", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def expected_group_names():
    """Имена групп, которые сводка ОБЯЗАНА публиковать.

    Считаются из разметки самого скрипта сводки (PUBLISHED_FROM), а не
    выписаны здесь руками: список, переписанный руками, разошёлся бы со
    скриптом молча. Оси, снимаемые дважды, дают ещё и группы первого
    прогона — с тем же именем и вставкой `run1`.
    """
    module = run_manifest_module()
    names = set()
    for axis, fields in module.PUBLISHED_FROM.items():
        for group in fields.values():
            names.add(group)
            if axis in ("build", "repro", "siblings"):
                names.add(group.replace(axis + "_", axis + "_run1_", 1))
    return names


def load():
    if not os.path.isdir(MANIFESTS):
        return {}
    out = {}
    for name in sorted(os.listdir(MANIFESTS)):
        if not name.endswith(".json"):
            continue
        with io.open(os.path.join(MANIFESTS, name), encoding="utf-8") as handle:
            out[name[:-5]] = json.loads(handle.read())
    return out


class Manifests(unittest.TestCase):
    def test_at_least_one_manifest_exists(self):
        self.assertTrue(load(),
                        "нет ни одной сводки прогона: сверять нечего. "
                        "Снимается `python3 scripts/run-manifest.py`.")

    def test_every_manifest_names_its_platform_and_versions(self):
        manifests = load()
        # Цикл по нулю сводок прошёл бы, ничего не проверив: зелёная от
        # отсутствия предмета проверка не работает.
        self.assertTrue(manifests, "нет ни одной сводки прогона")
        for name, manifest in manifests.items():
            env = manifest["environment"]
            for field in ("platform", "platform_full", "python", "docker",
                          "stand_root"):
                self.assertTrue(str(env.get(field, "")).strip(),
                                "%s: не записано поле %s" % (name, field))

    def test_the_exclusion_list_explains_every_field(self):
        manifests = load()
        self.assertTrue(manifests, "нет ни одной сводки прогона")
        for name, manifest in manifests.items():
            excluded = manifest["not_compared"]
            self.assertTrue(excluded, "%s: список исключений пуст" % name)
            for field, why in excluded.items():
                self.assertTrue(
                    str(why).strip(),
                    "%s: поле %s исключено из сравнения без объяснения — "
                    "молчаливое исключение превращает сравнение в фикцию"
                    % (name, field))

    def test_no_timing_value_is_compared(self):
        """Времена в сравнение не попадают вовсе.

        Проверка независима от того, что написано в списке исключений:
        она смотрит на сами величины сводки. Время, попавшее в
        сравниваемые величины, краснело бы на верных данных, и первым
        побуждением было бы «ослабить сверку».
        """
        manifests = load()
        self.assertTrue(manifests, "нет ни одной сводки прогона")
        for name, manifest in manifests.items():
            for key in manifest["values"]:
                for marker in TIME_MARKERS:
                    self.assertNotIn(
                        marker, key,
                        "%s: величина %r похожа на время и не должна "
                        "сравниваться между платформами" % (name, key))

    def test_published_values_agree_across_platforms(self):
        manifests = load()
        if len(manifests) < 2:
            self.skipTest("сводка одна: о совпадении между платформами "
                          "утверждать нечего")
        names = sorted(manifests)
        first = manifests[names[0]]["values"]
        for other in names[1:]:
            second = manifests[other]["values"]
            self.assertEqual(
                sorted(first), sorted(second),
                "%s и %s описывают разные наборы величин" % (names[0], other))
            tolerances = run_manifest_module().TOLERANCES
            for key in sorted(first):
                if key in tolerances:
                    self._agree_within(key, first[key], second[key],
                                       tolerances[key][0], names[0], other)
                    continue
                self.assertEqual(
                    first[key], second[key],
                    "величина %r различается между %s и %s — значит числа "
                    "стенда зависят от платформы сборки, и это обязано быть "
                    "сказано, а не спрятано" % (key, names[0], other))

    def _agree_within(self, key, first, second, limit, name_a, name_b):
        """Величина с допуском: сравнивается |Δ|, а не равенство.

        Округление, стоявшее здесь раньше, допуском не было: оно молчало
        до границы разряда и краснело сразу за ней, а запас до границы
        зависел от самого числа — у плеча cpp-nofmt он был 21 байт при
        наблюдаемом межплатформенном сдвиге 2–5 байт. Допуск назван
        числом и от того, куда попало число, не зависит.
        """
        self.assertEqual(sorted(first), sorted(second),
                         "величина %r описывает разные плечи" % key)
        for arm in sorted(first):
            delta = abs(first[arm] - second[arm])
            self.assertLessEqual(
                delta, limit,
                "величина %r у плеча %s разошлась между %s и %s на %d Б "
                "при допуске %d Б: допуск покрывает разницу УПАКОВКИ "
                "(биты прав доступа файлов контекста сборки), а не разницу "
                "содержимого — содержимое стерегут точные величины рядом"
                % (key, arm, name_a, name_b, delta, limit))

    def test_every_tolerance_explains_itself(self):
        """Допуск без объяснения — то же молчаливое исключение."""
        module = run_manifest_module()
        self.assertTrue(module.TOLERANCES, "список допусков пуст")
        for name, (limit, why) in module.TOLERANCES.items():
            self.assertGreater(limit, 0, "%s: допуск не положителен" % name)
            self.assertTrue(str(why).strip(),
                            "%s: допуск назначен без объяснения" % name)
        manifests = load()
        self.assertTrue(manifests, "нет ни одной сводки прогона")
        for name, manifest in manifests.items():
            declared = manifest.get("compared_with_tolerance") or {}
            self.assertEqual(
                sorted(declared), sorted(module.TOLERANCES),
                "%s: сводка называет не те величины с допуском, что скрипт"
                % name)

    def test_every_tolerated_value_has_an_exact_size_companion(self):
        """Рядом с величиной под допуском обязана стоять точная размерная.

        Огрубление допустимо НЕ потому, что «настоящее изменение сдвинуло
        бы число сильнее»: это неверно и опровергнуто собственной
        диверсией стенда — дописанные к бинарнику 8 байт сдвинули
        упакованный размер на 2 байта, причём вниз. Допустимо оно
        потому, что содержимое стережёт точно сравниваемая размерная
        величина той же оси.
        """
        companions = {
            "package_image_bytes": "package_image_unpacked_bytes",
            "siblings_image_registry_bytes": "siblings_image_unpacked_bytes",
            "siblings_run1_image_registry_bytes":
                "siblings_run1_image_unpacked_bytes",
        }
        module = run_manifest_module()
        self.assertEqual(
            sorted(companions), sorted(module.TOLERANCES),
            "у величины с допуском не назван точный спутник")
        manifests = load()
        self.assertTrue(manifests, "нет ни одной сводки прогона")
        for name, manifest in manifests.items():
            for tolerated, exact in companions.items():
                self.assertTrue(
                    exact in manifest["values"],
                    "%s: %r сравнивается с допуском, а точной размерной "
                    "величины (%s) рядом нет — содержимое образа не "
                    "стережёт никто" % (name, tolerated, exact))
                self.assertNotIn(
                    exact, module.TOLERANCES,
                    "%s: спутник %r сам получил допуск" % (name, exact))

    def test_the_manifest_publishes_everything_the_analyses_print(self):
        """Величина не должна выпасть из сводки молча.

        Сейчас пропажу ловит только несовпадение наборов ключей у двух
        сводок, и то пока одна из них заморожена: после полного прогона
        на обеих платформах обе потеряли бы величину разом, и сверка
        осталась бы зелёной. Здесь состав сводки сверяется с разметкой
        скрипта, а разметка — с полями самих фикстур (проверка ниже).
        """
        manifests = load()
        self.assertTrue(manifests, "нет ни одной сводки прогона")
        wanted = expected_group_names()
        for name, manifest in manifests.items():
            self.assertEqual(
                sorted(manifest["values"]), sorted(wanted),
                "%s: состав сводки разошёлся с разметкой PUBLISHED_FROM — "
                "величина выпала из публикации или появилась без разметки"
                % name)

    def test_every_fixture_field_is_published_or_explained(self):
        """Каждое поле фикстуры либо публикуется, либо объяснено.

        Иначе величину можно убрать из сводки и не сказать об этом
        нигде: сверка сравнивает то, что в сводках есть, и об
        отсутствующем не знает ничего.
        """
        module = run_manifest_module()
        axes = {
            "package": ["package.jsonl"],
            "build": ["build.jsonl", "build.run-1.jsonl"],
            "repro": ["repro.jsonl", "repro.run-1.jsonl"],
            "siblings": ["siblings.jsonl", "siblings.run-1.jsonl"],
        }
        fixtures = os.path.join(HERE, "fixtures")
        for axis, files in sorted(axes.items()):
            fields = set()
            for name in files:
                with io.open(os.path.join(fixtures, name),
                             encoding="utf-8") as handle:
                    for line in handle:
                        if line.strip().startswith("{"):
                            fields |= set(json.loads(line))
            self.assertTrue(fields, "ось %s: в фикстурах нет полей" % axis)
            for field in sorted(fields):
                where = []
                if field in module.PUBLISHED_FROM[axis]:
                    where.append("published")
                if field in module.NOT_PUBLISHED[axis]:
                    where.append("not_published")
                    self.assertTrue(
                        module.NOT_PUBLISHED[axis][field].strip(),
                        "ось %s: поле %s не публикуется без объяснения"
                        % (axis, field))
                if field in module.NOT_COMPARED:
                    where.append("not_compared")
                self.assertTrue(
                    where,
                    "ось %s: поле %s не публикуется, не объявлено "
                    "непубликуемым и не исключено из сравнения — величина "
                    "может пропасть из сводки молча" % (axis, field))

    def test_the_witness_does_not_assume_the_directory_name(self):
        """Свидетель не должен зависеть от имени каталога стенда.

        Первая редакция искала в командной строке каталог с именем
        `build-and-package`. На втором хосте стенд лежал в каталоге с
        другим именем — корней нашлось ноль, и сводка не снялась вовсе,
        уронив сводный прогон на предпоследнем шаге. Имя каталога не
        свойство стенда: читатель волен клонировать репозиторий куда
        угодно.
        """
        import importlib.util

        path = os.path.join(HERE, "scripts", "run-manifest.py")
        spec = importlib.util.spec_from_file_location("run_manifest", path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)

        root = module.stand_root()
        self.assertTrue(root, "свидетель платформы пуст")
        self.assertNotIn(
            "build-and-package", module.__dict__.get("STAND_DIR_NAME", ""),
            "имя каталога стенда снова зашито в код")
        self.assertNotIn(
            "STAND_DIR_NAME", module.__dict__,
            "свидетель снова опирается на имя каталога, а не на аргумент "
            "контекста сборки")

    def test_the_excluded_witness_really_differs(self):
        """Свидетель платформы обязан различаться у разных сводок.

        Корень пути стенда записан в командах `docker build` внутри самих
        фикстур. Совпал — значит фикстуры не переснимались, их прочитали
        дважды с одного диска, и «две платформы» были бы выдумкой.
        """
        manifests = load()
        if len(manifests) < 2:
            self.skipTest("сводка одна")
        roots = {m["environment"]["stand_root"] for m in manifests.values()}
        self.assertEqual(
            len(roots), len(manifests),
            "корень пути стенда одинаков у разных сводок: фикстуры сняты "
            "одним прогоном и лишь прочитаны дважды. Вывод о совпадении "
            "между платформами ложен.")
        for name, manifest in manifests.items():
            self.assertIn(
                "stand_root", manifest["not_compared"],
                "%s: свидетель платформы исключён из сравнения молча" % name)


if __name__ == "__main__":
    unittest.main()
