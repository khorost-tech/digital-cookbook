"""Задача 1: Go и Java говорят на одном протоколе.

Фикстура fixtures/protocol.jsonl — наблюдения одного набора запросов к обоим
серверам; они обязаны совпасть и не быть пустыми. С STAND_WITH_DOCKER=1 тот же
опыт ставится заново на свежих инстансах.
"""

import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "protocol.jsonl")


def read():
    text = open(FIXTURE, encoding="utf-8").read()
    if text.strip().splitlines()[-1] != END_MARKER:
        raise ValueError("protocol.jsonl: нет маркера завершения")
    return {r["variant"]: r["observations"] for r in map(json.loads, (l for l in text.splitlines()
                                                                      if l.startswith("{")))}


def check_not_empty(obs, test, kind):
    # Совпадение двух пустых наборов ничего не доказывает.
    test.assertEqual(obs["sse_ids"], [1, 2, 3], kind)
    test.assertEqual(obs["ws_ids"], [1, 2, 3], kind)
    test.assertEqual(obs["publish_ok"], 204, kind)
    test.assertTrue(obs["sse_instance_header"] and obs["ws_instance_header"], kind)


class ProtocolFixture(unittest.TestCase):
    def test_same_observations(self):
        obs = read()
        self.assertEqual(obs["go"], obs["java"])

    def test_observations_are_not_empty(self):
        for kind, obs in read().items():
            check_not_empty(obs, self, kind)


@unittest.skipUnless(os.environ.get("STAND_WITH_DOCKER") == "1", "нужен Docker: STAND_WITH_DOCKER=1")
class ProtocolLive(unittest.TestCase):
    def test_same_observations_now(self):
        docker.network()
        obs = {}
        for kind in ("go", "java"):
            name = docker.server("rt-parity-" + kind, kind)
            obs[kind] = docker.probe("check", "-url", "http://%s:8080" % name)
            docker.rm(name)
        self.assertEqual(obs["go"], obs["java"])
        check_not_empty(obs["go"], self, "go")


if __name__ == "__main__":
    unittest.main()
