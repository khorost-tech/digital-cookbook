"""Ось model для Locust. MODE=closed — пользователь шлёт следующий запрос сразу
после ответа; MODE=throughput — constant_throughput(RATE/USERS) на пользователя:
темп задан, но пользователь всё равно ждёт ответ (модель остаётся закрытой)."""

import os

from locust import HttpUser, constant_throughput, task

USERS = int(os.environ.get("USERS", "2"))
RATE = float(os.environ.get("RATE", "200"))


class Worker(HttpUser):
    if os.environ.get("MODE", "closed") == "throughput":
        wait_time = constant_throughput(RATE / USERS)

    @task
    def work(self):
        self.client.get(os.environ.get("TARGET_PATH", "/work"), name="work")
