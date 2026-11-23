"""Проверки оси 3 (задача 4 плана 30.09)."""

import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location(
    "analyze_shutdown", os.path.join(HERE, "scripts", "analyze-shutdown.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)


class Shutdown(unittest.TestCase):
    def test_controls_without_signal_serve_everyone(self):
        for row in analyze.controls(analyze.read()):
            self.assertEqual(row["dropped"], 0, row["variant"])

    def test_signal_before_anything_in_flight_is_refused(self):
        row = {"kind": "shutdown", "variant": "drain-enough",
               "probe": "sigterm", "sent": 4, "in_flight_at_signal": 0,
               "served": 4, "dropped": 0, "exit_code": 0,
               "accepted_after_signal": False}
        with self.assertRaises(SystemExit):
            analyze.check_rows([row])

    def test_served_plus_dropped_equals_sent(self):
        for row in analyze.read():
            self.assertEqual(row["served"] + row["dropped"], row["sent"], row["variant"])

    def test_every_signalled_row_had_everyone_in_flight(self):
        analyze.check_rows(analyze.read())

    def test_varying_variants_are_marked(self):
        # Вариант, чей счёт различался между сессиями (сводки в
        # fixtures/manifests), обязан быть помечен nondeterministic: иначе его
        # различие сравнение сессий примет за поломку — или, хуже, совпавший
        # по случаю счёт выдаст за устойчивый.
        import glob
        import json
        seen = {}
        for path in sorted(glob.glob(os.path.join(HERE, "fixtures", "manifests", "*.json"))):
            with open(path, encoding="utf-8") as f:
                for name, v in json.load(f)["published"]["shutdown"].items():
                    # Счёт сравним только при том же числе повторов.
                    key = (name, v.get("repeats", 1))
                    seen.setdefault(key, set()).add(v.get("served", v.get("served~")))
        marked = {r["variant"] for r in analyze.read() if r.get("nondeterministic")}
        for (name, _), values in seen.items():
            if len(values) > 1:
                self.assertIn(name, marked, "%s: счёт различался между сессиями %s" % (name, values))

    def test_variants(self):
        self.assertEqual({r["variant"] for r in analyze.read()},
                         {"control-no-signal", "no-handler", "no-handler-init",
                          "drain-enough", "drain-short", "control-slow-reader",
                          "control-slow-reader-halfclose", "drain-slow-after-ack",
                          "drain-slow-after-ack-halfclose", "drain-slow-after-close",
                          "drain-slow-after-close-halfclose", "drain-slow-after-write",
                          "drain-slow-before-write"})


if __name__ == "__main__":
    unittest.main()


class Guards(unittest.TestCase):
    def test_imbalance_is_refused(self):
        row = {"variant": "x", "probe": "sigterm", "sent": 4, "in_flight_at_signal": 4,
               "served": 1, "dropped": 0}
        with self.assertRaises(SystemExit):
            analyze.check_rows([row])

    def test_drain_handler_closes_accept(self):
        # Обработчик сигнала обязан закрыть приём ДО дослуживания: иначе
        # «новые не принимаются» было бы ложью. Читается по тексту сервиса.
        with open(os.path.join(HERE, "service", "main.cpp"), encoding="utf-8") as f:
            text = f.read()
        handler = text[text.index("signals.async_wait"):text.index("io.run()")]
        self.assertLess(handler.index("acceptor.close"), handler.index("(*poll)();"))

    def test_work_released_after_ack(self):
        # По умолчанию /work снимается с учёта, когда очередь отправки сокета
        # пуста (ядро клиента подтвердило ответ), — не до отправки, не по
        # завершении async_write и не по EOF от клиента.
        with open(os.path.join(HERE, "service", "main.cpp"), encoding="utf-8") as f:
            text = f.read()
        write = text[text.index("asio::async_write"):text.index("tcp::socket socket_;")]
        self.assertIn("--in_flight;                        // ответ в ядре клиента — дослужен", write)
        self.assertIn('env_or("STAND_INFLIGHT_RELEASE", "after-ack")', text)
