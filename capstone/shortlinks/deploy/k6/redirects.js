// Постоянная частота редиректов по случайным кодам из заранее созданных ссылок.
import http from 'k6/http';
import { check } from 'k6';

const codes = JSON.parse(open('/scripts/codes.json'));
export const options = {
  scenarios: { redirects: { executor: 'constant-arrival-rate', rate: Number(__ENV.RATE || 1500), timeUnit: '1s',
                            duration: __ENV.DURATION || '30s', preAllocatedVUs: 200, maxVUs: 1000 } },
  discardResponseBodies: true,
  summaryTrendStats: ['med', 'p(99)'],
};
export default function () {
  const code = codes[Math.floor(Math.random() * codes.length)];
  const r = http.get(`http://service:8080/${code}`, { redirects: 0 });
  check(r, { '302': (x) => x.status === 302 });
}
export function handleSummary(d) {
  const dur = d.metrics.http_req_duration.values;
  // dropped_iterations: constant-arrival-rate не гарантирует частоту — без свободных VU итерации пропускаются
  const dropped = d.metrics.dropped_iterations ? d.metrics.dropped_iterations.values.count : 0;
  return { stdout: JSON.stringify({ p50: dur.med, p99: dur['p(99)'], failed: d.metrics.http_req_failed.values.rate,
                                     checks: d.metrics.checks.values.rate, reqs: d.metrics.http_reqs.values.count,
                                     dropped: dropped }) + '\n' };
}
