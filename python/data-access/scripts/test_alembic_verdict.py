"""Предохранители разбора Alembic — вместе со случаями, которые их роняют.

Сценарий с ответами «да/нет» особенно легко испортить незаметно: таблица
из галочек выглядит убедительно независимо от того, что за ней стоит.
Здесь подсовываются подделки, которые прошли бы за успех.
"""

import importlib.util
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location(
    "analyze_alembic", ROOT / "scripts" / "analyze-alembic.py")
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)


def row(case, mode="умолчания", detected=True, applied=True, restored=True,
        changed=True, ops=("AddColumnOp",), error=None):
    return {"case": case, "mode": mode, "detected": detected,
            "applied": applied, "restored": restored,
            "upgrade_changed_schema": changed, "operations": list(ops),
            "rendered": "", "error": error, "alembic": "1.19.1",
            "checked_at": "2026-08-24T00:00:00+00:00"}


def healthy():
    return [
        row("контроль: изменений нет", detected=False, applied=None,
            restored=None, changed=None, ops=()),
        row("столбец с server_default"),
        row("смена умолчания у столбца", detected=False, applied=None,
            restored=None, changed=None, ops=()),
        row("переименование столбца", applied=False, restored=None,
            changed=None, error="IntegrityError: ..."),
    ]


class TestVerdict(unittest.TestCase):
    def test_healthy_fixture_passes(self):
        code, problems = mod.verdict(healthy())
        self.assertEqual(problems, [])
        self.assertEqual(code, 0)

    def test_missing_control_is_caught(self):
        data = [r for r in healthy() if not r["case"].startswith("контроль")]
        code, problems = mod.verdict(data)
        self.assertEqual(code, 2)
        self.assertTrue(any("контрольного случая нет" in p for p in problems))

    def test_dirty_control_is_caught(self):
        # Описание разошлось с базой: автогенерация предлагает операции там,
        # где ничего не менялось. Тогда лишние операции подмешаны в КАЖДЫЙ
        # случай, и вся таблица — мусор.
        data = healthy()
        data[0] = row("контроль: изменений нет", detected=True, applied=None,
                      restored=None, changed=None, ops=("DropIndexOp",))
        code, problems = mod.verdict(data)
        self.assertEqual(code, 2)
        self.assertTrue(any("контроль" in p and "DropIndexOp" in p
                            for p in problems), problems)

    def test_migration_that_changed_nothing_is_caught(self):
        # Миграция «применилась», но схема осталась прежней: проверялось не
        # то, что заявлено, а галочка в таблице стояла бы.
        data = healthy()
        data.append(row("частичный индекс", changed=False))
        code, problems = mod.verdict(data)
        self.assertEqual(code, 2)
        self.assertTrue(any("схема не изменилась" in p for p in problems))

    def test_applied_without_detection_is_caught(self):
        data = healthy()
        data.append(row("ограничение CHECK", detected=False, applied=True))
        code, problems = mod.verdict(data)
        self.assertEqual(code, 2)
        self.assertTrue(any("так не бывает" in p for p in problems))

    def test_not_detected_is_a_result_not_a_failure(self):
        # «Не видит» — законный результат сценария, а не дефект замера:
        # именно он и есть ответ про compare_server_default.
        code, problems = mod.verdict(healthy())
        self.assertEqual(problems, [])


if __name__ == "__main__":
    unittest.main(verbosity=2)
