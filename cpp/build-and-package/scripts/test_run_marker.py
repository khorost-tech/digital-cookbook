"""Маркер полного прогона обязан описывать ТО состояние, что лежит рядом.

Отметка времени, которую никто не сверяет, протухает молча: маркер стенда
был датирован коммитом, после которого набор вырос на пятнадцать
проверок, а фикстуры переснимались дважды, — и ни одна проверка его не
читала. Здесь он привязан к состоянию: суммы фикстур, число проверок,
число диверсий.

ОТСУТСТВИЕ МАРКЕРА — ДОПУСТИМОЕ СОСТОЯНИЕ, И ЭТО НЕ ЛАЗЕЙКА.
`bench/run-all.sh` снимает маркер ПЕРВЫМ делом и ставит последним: пока
прогон идёт, маркера нет, и это видно. Утверждение проверки узкое: если
маркер ЕСТЬ, он описывает эти самые фикстуры и этот самый набор
проверок. Прогон, оборванный на середине, маркера не оставит вовсе.
"""

import importlib.util
import io
import json
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MARKER_PATH = os.environ.get("RUN_MARKER",
                             os.path.join(HERE, "fixtures", "RUN-COMPLETE"))


def marker_module():
    path = os.path.join(HERE, "scripts", "run-marker.py")
    spec = importlib.util.spec_from_file_location("run_marker", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def load():
    if not os.path.exists(MARKER_PATH):
        return None
    with io.open(MARKER_PATH, encoding="utf-8") as handle:
        text = handle.read()
    return json.loads(text)


class RunMarker(unittest.TestCase):
    def test_marker_is_machine_readable(self):
        """Маркер — состояние, а не строка времени.

        Прежний маркер был одной строкой даты. Читать в ней было нечего,
        и сверять — тоже.
        """
        marker = load()
        if marker is None:
            self.skipTest("маркера нет: полный прогон не завершён "
                          "(bench/run-all.sh снимает его первым делом)")
        for field in ("finished_at", "platform", "fixtures_sha256",
                      "checks", "sabotages"):
            self.assertIn(field, marker,
                          "в маркере нет поля %s" % field)

    def test_marker_matches_the_fixtures_lying_next_to_it(self):
        """Правка фикстуры делает маркер красным, а не тихо устаревшим."""
        marker = load()
        if marker is None:
            self.skipTest("маркера нет: полный прогон не завершён")
        module = marker_module()
        now = module.fixture_digests()
        self.assertEqual(
            sorted(marker["fixtures_sha256"]), sorted(now),
            "маркер описывает не тот набор фикстур, что лежит в стенде")
        for name in sorted(now):
            self.assertEqual(
                marker["fixtures_sha256"][name], now[name],
                "фикстура %s изменилась после того, как прогон был объявлен "
                "полным: маркер описывает состояние, которого больше нет. "
                "Либо прогон повторяется целиком, либо маркер снимается."
                % name)

    def test_marker_matches_the_current_set_of_checks(self):
        """Новая проверка после прогона делает маркер красным.

        Ровно этого не хватало: маркер пережил рост набора на пятнадцать
        проверок и остался зелёным, потому что его никто не читал.
        """
        marker = load()
        if marker is None:
            self.skipTest("маркера нет: полный прогон не завершён")
        module = marker_module()
        self.assertEqual(
            marker["checks"], module.check_count(),
            "набор проверок изменился после того, как прогон был объявлен "
            "полным (%s в маркере против %s сейчас): маркер описывает не "
            "этот стенд" % (marker["checks"], module.check_count()))
        self.assertEqual(
            marker["sabotages"], module.sabotage_count(),
            "набор диверсий изменился после объявления прогона полным "
            "(%s против %s)" % (marker["sabotages"],
                                module.sabotage_count()))


if __name__ == "__main__":
    unittest.main()
