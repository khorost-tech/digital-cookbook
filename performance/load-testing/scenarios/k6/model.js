// Ось model: одна задача в двух моделях. MODE=closed — 2 VU без пауз (новый
// запрос только после ответа); MODE=open — 200 итераций/с независимо от ответов.
import http from 'k6/http';

const TARGET = __ENV.TARGET || 'http://svc:8080/work';
const DURATION = __ENV.DURATION || '60s';
const RATE = Number(__ENV.RATE || 200);

const scenarios = {
  closed: { executor: 'constant-vus', vus: Number(__ENV.VUS || 2), duration: DURATION },
  open: { executor: 'constant-arrival-rate', rate: RATE, timeUnit: '1s', duration: DURATION,
          preAllocatedVUs: Number(__ENV.PRE_VUS || 50), maxVUs: Number(__ENV.MAX_VUS || 400) },
};

export const options = {
  scenarios: { main: scenarios[__ENV.MODE || 'open'] },
  summaryTrendStats: ['p(50)', 'p(90)', 'p(99)', 'max'],
  discardResponseBodies: true,
};

export default function () {
  http.get(TARGET);
}

export function handleSummary(data) {
  const d = data.metrics.http_req_duration.values;
  const m = (k) => (data.metrics[k] ? data.metrics[k].values.count : 0);
  const r = { requests: m('http_reqs'), errors: data.metrics.http_req_failed.values.passes,
              dropped_iterations: m('dropped_iterations'),
              p50_ms: d['p(50)'], p90_ms: d['p(90)'], p99_ms: d['p(99)'], max_ms: d.max };
  return { stdout: 'REPORT ' + JSON.stringify(r) + '\n' };
}
