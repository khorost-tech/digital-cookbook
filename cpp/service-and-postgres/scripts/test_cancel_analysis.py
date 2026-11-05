"""Проверки оси 4 — переживает ли запрос уход клиента.

НАПИСАНЫ ДО РЕАЛИЗАЦИИ и на своём коммите падают целиком: модуля
`scripts/cancel.py` ещё нет, фикстуры тоже. Это записанный шаг, а не
случайность.

ЧЕТЫРЕ ЛОВУШКИ, ПОД КОТОРЫЕ ЗДЕСЬ ПОДЛОЖЕНЫ ПРОВЕРКИ:

* КОНТРОЛЬ, ПРЕВРАЩЁННЫЙ В ФОРМАЛЬНОСТЬ. «После ответа активных ноль» —
  единственное, что отличает свой запрос от чужого. Если контроль
  разрешено пропустить (плеча нет, число не сверяется), ненулевой счёт у
  брошенного плеча не значит ничего;
* СЧЁТ НЕ ПО ИМЕНИ ВАРИАНТА. Активные запросы читаются по
  `application_name`; счёт без имени показал бы фоновые процессы базы и
  дал бы правдоподобное число ни о чём;
* НОЛЬ НАБЛЮДЕНИЙ ВМЕСТО НОЛЯ АКТИВНЫХ. «Запрос остался активным»
  требует, чтобы он был виден активным ДО обрыва, и чтобы к обрыву от
  сервиса не пришло ни байта. Иначе проба меряет не предмет;
* НАБЛЮДЕНИЕ, ПОДПИСАННОЕ КАК МЕХАНИЗМ. «Отмена не доходит до libpq» —
  объяснение, а не замер. В печати оно обязано идти под своей пометкой,
  и запрет живёт в коде, а не в договорённости.
"""

import io
import json
import os
import sys
import unittest

STAND = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if STAND not in sys.path:
    sys.path.insert(0, STAND)

from scripts import cancel  # noqa: E402
from scripts.outcome import END_MARKER  # noqa: E402

ARMS = ("beast", "crow", "drogon")


def _abandoned(variant="beast", **extra):
    """Придуманная строка брошенного плеча — чтобы правило можно было ронять.

    На настоящей фикстуре контроль сходится, и испорченное правило
    зеленело бы вместе с ней. Поэтому правило проверяется на строках,
    которых не было ни в одном прогоне.
    """
    row = {
        "kind": "cancel",
        "variant": variant,
        "arm": cancel.ABANDONED,
        "counted_application_name": variant,
        "sleep_ms": cancel.SLEEP_MS,
        "close_after_ms": cancel.CLOSE_AFTER_MS,
        "wait_after_close_ms": cancel.WAIT_AFTER_CLOSE_MS,
        "bytes_before_close": 0,
        "socket_closed": True,
        "active_before_close": 1,
        "active_after_close": 1,
        "active_after_window": 0,
        "queries_after_close": ["SELECT pg_sleep($1)"],
    }
    row.update(extra)
    return row


def _awaited(variant="beast", **extra):
    row = {
        "kind": "cancel",
        "variant": variant,
        "arm": cancel.AWAITED,
        "counted_application_name": variant,
        "sleep_ms": cancel.SLEEP_MS,
        "replied": True,
        "status": 200,
        "active_after_reply": 0,
        "queries_after_reply": [],
    }
    row.update(extra)
    return row


def _rows(**extra):
    rows = []
    for name in ARMS:
        rows.append(_awaited(variant=name))
        rows.append(_abandoned(variant=name, **extra))
    return rows


class TheControl(unittest.TestCase):
    """Контроль первым: без него ненулевой счёт не значит ничего."""

    def test_zero_after_the_reply_is_what_makes_the_axis_valid(self):
        self.assertTrue(cancel.verdict(_rows())["control_reads_zero"])

    def test_a_nonzero_control_kills_the_axis(self):
        rows = _rows()
        rows[0]["active_after_reply"] = 1
        decision = cancel.verdict(rows)
        self.assertFalse(decision["control_reads_zero"])
        self.assertFalse(decision["cancel_publishable"])

    def test_a_missing_control_arm_is_loud(self):
        # Пропуск контроля — не «нет данных», а отказ: половина пары
        # контролем не является.
        rows = [row for row in _rows() if row["arm"] != cancel.AWAITED]
        with self.assertRaises(KeyError):
            cancel.verdict(rows)

    def test_a_control_that_never_got_a_reply_is_not_a_control(self):
        rows = _rows()
        rows[0]["replied"] = False
        self.assertFalse(cancel.verdict(rows)["cancel_publishable"])


class TheCountIsTiedToTheVariant(unittest.TestCase):
    """Активные запросы считаются ПО ИМЕНИ ВАРИАНТА, и это видно в строке."""

    def test_the_counter_asks_the_database_by_the_variant_name(self):
        asked = []

        class FakeDatabase(object):
            def active_queries(self, application_name):
                asked.append(application_name)
                return [{"pid": 1, "state": "active",
                         "query": "SELECT pg_sleep($1)"}]

        count, texts = cancel.count_active(FakeDatabase(), "crow")
        self.assertEqual(asked, ["crow"])
        self.assertEqual(count, 1)
        self.assertEqual(texts, ["SELECT pg_sleep($1)"])

    def test_a_row_counted_under_another_name_kills_the_axis(self):
        rows = _rows()
        rows[1]["counted_application_name"] = "postgres"
        decision = cancel.verdict(rows)
        self.assertFalse(decision["every_count_is_by_the_variant_name"])
        self.assertFalse(decision["cancel_publishable"])


class TheProbeMustHaveMeasuredThePoint(unittest.TestCase):
    """Ноль наблюдений — не ноль активных, и уход обязан быть до ответа."""

    def test_an_unseen_query_kills_the_axis(self):
        decision = cancel.verdict(_rows(active_before_close=0))
        self.assertFalse(decision["query_was_seen_active"])
        self.assertFalse(decision["cancel_publishable"])

    def test_bytes_received_before_the_close_kill_the_axis(self):
        decision = cancel.verdict(_rows(bytes_before_close=17))
        self.assertFalse(decision["client_left_before_the_reply"])
        self.assertFalse(decision["cancel_publishable"])

    def test_a_socket_that_was_not_closed_kills_the_axis(self):
        self.assertFalse(
            cancel.verdict(_rows(socket_closed=False))["cancel_publishable"])

    def test_a_query_that_never_ends_is_a_finding_and_is_named(self):
        decision = cancel.verdict(_rows(active_after_window=1))
        self.assertFalse(decision["the_query_ends_on_its_own"])


class TheObservation(unittest.TestCase):
    """Сам предмет: остался ли запрос активным — по варианту и в целом."""

    def test_a_surviving_query_is_recorded_per_variant(self):
        decision = cancel.verdict(_rows())
        self.assertEqual(decision["survives_by_variant"],
                         {"beast": True, "crow": True, "drogon": True})
        self.assertTrue(decision["survives_everywhere"])

    def test_a_vanished_query_is_recorded_as_such_and_the_axis_still_holds(
            self):
        # Опровержение ожидания — законный исход, а не поломка: ось
        # остаётся действительной, меняется только наблюдение.
        decision = cancel.verdict(_rows(active_after_close=0))
        self.assertFalse(decision["survives_everywhere"])
        self.assertTrue(decision["cancel_publishable"])


class TheExplanationIsLabelled(unittest.TestCase):
    """Объяснение печатается только под пометкой — запрет живёт в коде."""

    def test_the_report_marks_the_explanation_as_an_explanation(self):
        lines = cancel.report(_rows())
        mechanism = [line for line in lines
                     if any(word in line for word in cancel.MECHANISM_WORDS)]
        self.assertTrue(mechanism, "объяснение вовсе не напечатано")
        for line in mechanism:
            self.assertIn(cancel.EXPLANATION_LABEL, "\n".join(lines))

    def test_an_unlabelled_explanation_is_refused(self):
        with self.assertRaises(ValueError):
            cancel.check_explanation_is_labelled(
                "Отмена не доходит до libpq, поэтому запрос доживает.")

    def test_a_labelled_explanation_passes(self):
        cancel.check_explanation_is_labelled(
            "%s: отмена не доходит до libpq — это не замерено."
            % cancel.EXPLANATION_LABEL)

    def test_the_report_refuses_to_print_an_invalid_axis(self):
        # Сценарий — самое удобное место обойти запрет; поэтому он здесь.
        rows = _rows()
        rows[0]["active_after_reply"] = 2
        with self.assertRaises(ValueError):
            cancel.report(rows)


class TheFixture(unittest.TestCase):
    """Снятый замер читается тем же кодом и проходит те же условия."""

    def test_the_fixture_is_valid_and_complete(self):
        if not os.path.exists(cancel.FIXTURE):
            self.skipTest("замер оси 4 ещё не снят")
        with io.open(cancel.FIXTURE, encoding="utf-8") as handle:
            text = handle.read()
        self.assertTrue(text.strip().endswith(END_MARKER),
                        "оборванный прогон не должен выглядеть полным")
        rows = cancel.read(cancel.FIXTURE)
        decision = cancel.verdict(rows)
        self.assertTrue(decision["cancel_publishable"],
                        json.dumps(decision, ensure_ascii=False))


if __name__ == "__main__":
    unittest.main()
