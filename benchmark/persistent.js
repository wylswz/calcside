import { check } from 'k6';
import http from 'k6/http';
import { Counter, Rate, Trend } from 'k6/metrics';

function positive(name, fallback) {
  const n = Number(__ENV[name] || fallback);
  if (!Number.isSafeInteger(n) || n < 1) throw new Error(`Invalid ${name}`);
  return n;
}

const vus = positive('SCALABILITY_VUS', 8);
const duration = __ENV.SCALABILITY_DURATION || '30s';
const expected = Number(__ENV.SCALABILITY_ANSWER);
const base = __ENV.CALCSIDE_BASE_URL;
const app = __ENV.SCALABILITY_APP_ID;
if (!base || !app || !Number.isSafeInteger(expected)) throw new Error('Missing fixture configuration');
const input = __ENV.SCALABILITY_INPUT || '{}';
const workerIPs = JSON.parse(__ENV.SCALABILITY_WORKER_IPS || '{}');
const apiIPs = JSON.parse(__ENV.SCALABILITY_API_IPS || '{}');

const attempts = new Counter('scalability_attempts');
const completed = new Counter('scalability_completed');
const success = new Rate('scalability_success');
const latency = new Trend('scalability_latency_ms', true);
const goodLatency = new Trend('scalability_success_latency_ms', true);
const runtimeErrors = new Counter('scalability_runtime_errors');
const transportErrors = new Counter('scalability_transport_errors');
const httpErrors = new Counter('scalability_http_errors');
const unknownWorker = new Counter('scalability_worker_unknown');
const workers = Array.from({ length: 3 }, (_, i) => new Counter(`scalability_worker_${i}`));
const apis = Array.from({ length: 2 }, (_, i) => new Counter(`scalability_api_${i}`));
const unknownAPI = new Counter('scalability_api_unknown');

export const options = {
  scenarios: { invoke: { executor: 'constant-vus', vus, duration, gracefulStop: '20s' } },
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(95)', 'p(99)'],
  thresholds: __ENV.SCALABILITY_ALLOW_FAILURES === '1'
    ? { scalability_attempts: ['count>0'] }
    : { scalability_success: ['rate==1'], scalability_completed: ['count>0'] },
};

export default function () {
  const start = Date.now();
  const r = http.post(`${base}/api/v1/applications/${encodeURIComponent(app)}/invoke`, input, {
    headers: {
      Authorization: `Bearer ${__ENV.CALCSIDE_API_KEY}`,
      'Content-Type': 'application/json',
      'X-Requested-With': 'calcside',
    },
    tags: { name: 'persistent-invoke' },
    timeout: '15s',
    redirects: 0,
  });
  const elapsed = Date.now() - start;
  let body = null;
  let output = null;
  try {
    body = r.json();
    output = JSON.parse(body.output_json);
  } catch { /* Malformed or missing output is a failed invocation. */ }
  const valid = r.status === 200 && !body?.result?.error
    && output?.answer === expected && output?.fresh === true;
  attempts.add(1);
  success.add(valid);
  latency.add(elapsed);
  check(r, { 'HTTP and application result are valid': () => valid });
  if (valid) {
    completed.add(1);
    goodLatency.add(elapsed);
  }
  if (r.status === 0) transportErrors.add(1);
  else if (r.status !== 200) httpErrors.add(1);
  else if (body?.result?.error) runtimeErrors.add(1);

  const api = r.headers['X-Scalability-Api'] || r.headers['X-Scalability-API'] || '';
  const ai = apiIPs[api];
  if (ai === undefined) unknownAPI.add(1);
  else apis[ai].add(1);
  // The I/O fixture returns the caller's pod IP from the delay server.
  // CPU runs deliberately avoid that extra network operation.
  if (output?.worker !== undefined) {
    const wi = workerIPs[output.worker];
    if (wi === undefined) unknownWorker.add(1);
    else workers[wi].add(1);
  }
  if (__ENV.SCALABILITY_TRACE === '1') {
    console.log(`SCALABILITY_EVENT ${JSON.stringify({
      started_ms: start, duration_ms: elapsed, ok: valid, status: r.status,
      api, worker: output?.worker || null, error: body?.result?.error?.code || null,
    })}`);
  }
}

export function handleSummary(data) {
  return {
    [__ENV.SCALABILITY_SUMMARY]: JSON.stringify({
      schema: 1, vus, duration, state: data.state, metrics: data.metrics,
    }, null, 2),
  };
}
