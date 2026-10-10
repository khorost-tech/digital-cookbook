// Точка входа: без аргументов — воркер, с аргументом `run` — запуск
// воркфлоу, отправка сигнала и печать итога.

import { Worker, NativeConnection } from '@temporalio/worker';
import { Client, Connection } from '@temporalio/client';
import * as activities from './activities';
import { crossLangWorkflow, confirmSignal } from './workflow';

const address = process.env.TEMPORAL_ADDRESS ?? 'temporal-frontend:7233';
const taskQueue = process.env.TASK_QUEUE ?? 'lang-ts-tq';
// Версия берётся из установленного пакета, а не из package.json:
// в лог должно попасть то, что реально загружено.
// eslint-disable-next-line @typescript-eslint/no-var-requires
const sdk = require('@temporalio/worker/package.json').version as string;

async function runWorker(): Promise<void> {
  const connection = await NativeConnection.connect({ address });
  const worker = await Worker.create({
    connection,
    taskQueue,
    workflowsPath: require.resolve('./workflow'),
    activities,
  });
  console.log(`[worker] lang=ts sdk=${sdk} queue=${taskQueue}`);
  await worker.run();
}

async function runWorkflow(approve: boolean): Promise<void> {
  const connection = await Connection.connect({ address });
  const client = new Client({ connection });

  const handle = await client.workflow.start(crossLangWorkflow, {
    args: ['gpu-node-7'],
    taskQueue,
    workflowId: 'lang-ts',
  });

  await new Promise((r) => setTimeout(r, 4000));
  await handle.signal(confirmSignal, approve);

  const outcome = await handle.result();
  console.log(`ЯЗЫК lang=ts sdk=${sdk} queue=${taskQueue} outcome=${outcome}`);
  await connection.close();
}

const mode = process.argv[2];
const approve = process.argv.includes('--approve');

(mode === 'run' ? runWorkflow(approve) : runWorker()).catch((err) => {
  console.error(err);
  process.exit(1);
});
