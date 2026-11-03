// Ось types: виды нагрузочных тестов одним сценарием, TYPE — какой.
//   load   — 400/с (50% ёмкости C/S = 800/с), 60 с: выполняется ли SLO
//   stress — темп растёт 200 → 1000/с за 60 с: где колено
//   spike  — 400/с, на 20-й секунде всплеск 1200/с на 5 с: сколько разгружается очередь
//   soak   — 100/с на /work?leak=1 (сервис с утечкой), DURATION: 30 с или 300 с
import http from 'k6/http';

const BASE = __ENV.BASE || 'http://svc:8080';
const T = __ENV.TYPE || 'load';
const open = (o) => Object.assign({ timeUnit: '1s', preAllocatedVUs: 200, maxVUs: 2500 }, o);

const scenarios = {
  load: open({ executor: 'constant-arrival-rate', rate: 400, duration: '60s' }),
  // очередь всплеска держит до ≈2000 запросов в полёте: VU — заранее, иначе k6 не
  // успевает их создавать и молча недодаёт темп (dropped_iterations)
  stress: open({ executor: 'ramping-arrival-rate', startRate: 200, preAllocatedVUs: 2500,
                 stages: [{ target: 1000, duration: '60s' }] }),
  spike: open({ executor: 'ramping-arrival-rate', startRate: 400, preAllocatedVUs: Number(__ENV.PRE_VUS || 2500), stages: [
    { target: 400, duration: '20s' }, { target: 1200, duration: '1s' }, { target: 1200, duration: '5s' },
    { target: 400, duration: '1s' }, { target: 400, duration: '33s' }] }),
  soak: open({ executor: 'constant-arrival-rate', rate: 100, duration: __ENV.DURATION || '300s', preAllocatedVUs: 50 }),
};

export const options = {
  scenarios: { main: scenarios[T] },
  summaryTrendStats: ['p(50)', 'p(90)', 'p(99)', 'max'],
  discardResponseBodies: true,
  // SLO теста: p99 < 50 мс и ошибок < 1% — порог проверяется, тест не обрывает
  thresholds: { http_req_duration: ['p(99)<50'], http_req_failed: ['rate<0.01'] },
};

const PATH = T === 'soak' ? '/work?leak=1' : '/work';

export default function () {
  http.get(BASE + PATH, { timeout: '20s' });
}

export function handleSummary(data) {
  const d = data.metrics.http_req_duration.values;
  const m = (k) => (data.metrics[k] ? data.metrics[k].values.count : 0);
  const th = {};
  for (const [k, v] of Object.entries(data.metrics)) {
    if (v.thresholds) th[k] = Object.values(v.thresholds).every((t) => t.ok);
  }
  const r = { requests: m('http_reqs'), errors: data.metrics.http_req_failed.values.passes,
              dropped_iterations: m('dropped_iterations'), thresholds_ok: th,
              p50_ms: d['p(50)'], p90_ms: d['p(90)'], p99_ms: d['p(99)'], max_ms: d.max };
  return { stdout: 'REPORT ' + JSON.stringify(r) + '\n' };
}
