// Ось chaos: 200 запросов/с на /order через балансировщик, 60 с; сбой вносит
// раннер на 20-й секунде. Гипотеза стабильного состояния — пороги: ошибок < 1%,
// p99 < 300 мс. ABORT=1 — порог по ошибкам обрывает эксперимент (blast radius).
import http from 'k6/http';
import { Counter, Trend } from 'k6/metrics';

const fallback = new Counter('fallback_responses');
// возраст запасного ответа (последняя полученная цена), мс — из заголовка сервиса
const fallbackAge = new Trend('fallback_age_ms');
// полная операция пользователя: http.get целиком, со всеми повторами клиента.
// http_req_duration этого не гарантирует: при повторе соединения k6 перезаписывает
// отметки замера, и время прежней попытки из него выпадает
const op = new Trend('op_duration', true);
const abort = __ENV.ABORT === '1';

export const options = {
  scenarios: { main: { executor: 'constant-arrival-rate', rate: 200, timeUnit: '1s', duration: '60s',
                       preAllocatedVUs: 300, maxVUs: 1500 } },
  summaryTrendStats: ['p(50)', 'p(90)', 'p(99)', 'max'],
  // сырые точки (--out json) — только то, что нужно для окон сбоя
  systemTags: ['status', 'expected_response'],
  discardResponseBodies: true,
  thresholds: {
    http_req_failed: [{ threshold: 'rate<0.01', abortOnFail: abort, delayAbortEval: '1s' }],
    http_req_duration: ['p(99)<300'],
    op_duration: ['p(99)<300'],
  },
};

export default function () {
  const t0 = Date.now();
  const r = http.get(__ENV.TARGET || 'http://lb:8080/order', { timeout: '10s' });
  op.add(Date.now() - t0, { failed: String(r.status !== 200) });
  if (r.headers['X-Fallback'] === '1') {
    fallback.add(1);
    fallbackAge.add(Number(r.headers['X-Fallback-Age-Ms']));
  }
}

export function handleSummary(data) {
  const d = data.metrics.http_req_duration.values;
  const m = (k) => (data.metrics[k] ? data.metrics[k].values.count : 0);
  const th = {};
  for (const [k, v] of Object.entries(data.metrics)) {
    if (v.thresholds) th[k] = Object.values(v.thresholds).every((t) => t.ok);
  }
  const r = { requests: m('http_reqs'), errors: data.metrics.http_req_failed.values.passes,
              fallback: m('fallback_responses'), dropped_iterations: m('dropped_iterations'),
              fallback_age_p50_ms: data.metrics.fallback_age_ms ? data.metrics.fallback_age_ms.values['p(50)'] : null,
              fallback_age_max_ms: data.metrics.fallback_age_ms ? data.metrics.fallback_age_ms.values.max : null,
              thresholds_ok: th, run_ms: data.state.testRunDurationMs,
              p50_ms: d['p(50)'], p90_ms: d['p(90)'], p99_ms: d['p(99)'], max_ms: d.max,
              op_p99_ms: data.metrics.op_duration.values['p(99)'], op_max_ms: data.metrics.op_duration.values.max };
  return { stdout: 'REPORT ' + JSON.stringify(r) + '\n' };
}
