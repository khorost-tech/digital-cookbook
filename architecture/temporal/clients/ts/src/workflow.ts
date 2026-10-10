// Профиль 07-languages, часть TypeScript.
//
// Детерминизм здесь достигается иначе, чем в Go и Java: воркфлоу
// исполняется в ИЗОЛИРОВАННОМ окружении, где таймеры, промисы,
// Date.now() и Math.random() ПОДМЕНЕНЫ детерминированными версиями.
// Обычный код не запрещён — он перехвачен.

import { proxyActivities, defineSignal, setHandler, condition, sleep } from '@temporalio/workflow';
import type * as activities from './activities';

const { reserve, allocate, cancelReservation } = proxyActivities<typeof activities>({
  startToCloseTimeout: '30 seconds',
});

export const confirmSignal = defineSignal<[boolean]>('confirm');

export async function crossLangWorkflow(resource: string): Promise<string> {
  let approved: boolean | undefined;
  setHandler(confirmSignal, (value: boolean) => {
    approved = value;
  });

  const rid = await reserve(resource);

  // sleep — детерминированный таймер SDK, а не setTimeout.
  await sleep('2 seconds');

  // condition с таймаутом возвращает false, если условие не наступило.
  const got = await condition(() => approved !== undefined, '60 seconds');

  if (got && approved === true) {
    await allocate(rid);
    return 'allocated';
  }
  await cancelReservation(rid);
  return 'cancelled';
}
