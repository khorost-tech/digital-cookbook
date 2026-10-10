"""Профиль 07-languages, часть Python.

Тот же сценарий: activity -> таймер -> ожидание сигнала с таймаутом ->
компенсация при отказе.

Детерминизм в Python-SDK достигается собственным event loop: внутри
воркфлоу asyncio.sleep перехвачен и превращён в таймер Temporal, а
время и случайность берутся из детерминированных источников SDK.
"""

import asyncio
import os
import sys
from datetime import timedelta

import temporalio
from temporalio import activity, workflow
from temporalio.client import Client
from temporalio.worker import Worker

ADDRESS = os.environ.get("TEMPORAL_ADDRESS", "temporal-frontend:7233")
TASK_QUEUE = os.environ.get("TASK_QUEUE", "lang-python-tq")
SDK = temporalio.__version__


@activity.defn
async def reserve(resource: str) -> str:
    print(f">>> ACTIVITY reserve({resource})", flush=True)
    return f"res-{resource}-001"


@activity.defn
async def allocate(reservation_id: str) -> str:
    print(f">>> ACTIVITY allocate({reservation_id})", flush=True)
    return f"выделено {reservation_id}"


@activity.defn
async def cancel_reservation(reservation_id: str) -> str:
    print(f">>> ACTIVITY cancelReservation({reservation_id})", flush=True)
    return f"снято {reservation_id}"


@workflow.defn
class CrossLangWorkflow:
    def __init__(self) -> None:
        self._approved = None

    @workflow.signal(name="confirm")
    def confirm(self, approved: bool) -> None:
        self._approved = approved

    @workflow.run
    async def run(self, resource: str) -> str:
        rid = await workflow.execute_activity(
            reserve, resource, start_to_close_timeout=timedelta(seconds=30)
        )

        # asyncio.sleep внутри воркфлоу перехвачен SDK и становится
        # таймером Temporal — настоящего сна процесса здесь нет.
        await asyncio.sleep(2)

        try:
            await workflow.wait_condition(
                lambda: self._approved is not None, timeout=timedelta(seconds=60)
            )
        except TimeoutError:
            self._approved = None

        if self._approved is True:
            await workflow.execute_activity(
                allocate, rid, start_to_close_timeout=timedelta(seconds=30)
            )
            return "allocated"

        await workflow.execute_activity(
            cancel_reservation, rid, start_to_close_timeout=timedelta(seconds=30)
        )
        return "cancelled"


async def run_worker() -> None:
    client = await Client.connect(ADDRESS)
    async with Worker(
        client,
        task_queue=TASK_QUEUE,
        workflows=[CrossLangWorkflow],
        activities=[reserve, allocate, cancel_reservation],
    ):
        print(f"[worker] lang=python sdk={SDK} queue={TASK_QUEUE}", flush=True)
        await asyncio.Future()


async def run_workflow(approve: bool) -> None:
    client = await Client.connect(ADDRESS)
    handle = await client.start_workflow(
        CrossLangWorkflow.run,
        "gpu-node-7",
        id="lang-python",
        task_queue=TASK_QUEUE,
    )
    await asyncio.sleep(4)
    await handle.signal(CrossLangWorkflow.confirm, approve)
    outcome = await handle.result()
    print(f"ЯЗЫК lang=python sdk={SDK} queue={TASK_QUEUE} outcome={outcome}", flush=True)


if __name__ == "__main__":
    mode = sys.argv[1] if len(sys.argv) > 1 else "worker"
    approved = "--approve" in sys.argv
    asyncio.run(run_workflow(approved) if mode == "run" else run_worker())
