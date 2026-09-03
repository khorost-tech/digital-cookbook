"""Совпадение между платформами — либо доказано сводками, либо не заявлено.

Статья утверждала, что все числа совпали на двух платформах. Проверить
это было нечем: платформа в фикстурах не записана, а прогон
перезаписывает один и тот же набор файлов. Утверждение держалось на
памяти автора — ровно то, против чего построен весь стенд.

Теперь каждый прогон оставляет сводку по своей платформе, сводки
коммитятся, и сравнение делает машина.

Проверка НЕ требует, чтобы платформ было две. Она требует другого: если
сводок несколько, все опубликованные величины обязаны совпасть. Пока
сводка одна, утверждать о совпадении нечего — и об этом сказано вслух, а
не умолчано.
"""

import io
import json
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MANIFESTS = os.path.join(HERE, "fixtures/manifests")


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
        self.assertTrue(load(), "нет ни одной сводки прогона: нечего сверять")

    def test_every_manifest_names_its_platform_and_versions(self):
        for name, manifest in load().items():
            env = manifest["environment"]
            for field in ("platform", "platform_full", "python", "go_version"):
                self.assertTrue(str(env.get(field, "")).strip(),
                                "%s: не записано поле %s" % (name, field))

    def test_published_values_agree_across_platforms(self):
        """Если платформ больше одной — числа обязаны совпасть.

        Сравниваются ровно те величины, которые попадают в текст. Хеши
        файлов для этого не годятся: фикстуры содержат случайные поля
        рукопожатия и различаются при каждом прогоне даже на одной
        машине.
        """
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
            for key in sorted(first):
                self.assertEqual(
                    first[key], second[key],
                    "величина %r различается между %s и %s — значит числа "
                    "статьи зависят от платформы, и это надо сказать"
                    % (key, names[0], other))

    def test_the_excluded_field_is_named_and_really_differs(self):
        """Исключение из сравнения обязано быть явным и оправданным.

        Строка версии Go содержит имя платформы. Она исключена нарочно —
        но если сводок несколько и она вдруг СОВПАЛА, значит прогоны
        сняты на одной платформе, и вывод о двух был бы ложным.
        """
        manifests = load()
        if len(manifests) < 2:
            self.skipTest("сводка одна")
        versions = {m["environment"]["go_version"] for m in manifests.values()}
        self.assertEqual(
            len(versions), len(manifests),
            "строка версии Go одинакова у разных сводок: прогоны сняты на "
            "одной платформе, и вывод о совпадении между платформами ложен")
        for manifest in manifests.values():
            self.assertIn("go_version", manifest["not_compared"],
                          "поле исключено из сравнения молча")


if __name__ == "__main__":
    unittest.main()
