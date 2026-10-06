import { check, fail } from 'k6';
import execution from 'k6/execution';
import http from 'k6/http';
import { Counter, Rate, Trend } from 'k6/metrics';

function positiveInteger(name, fallback) {
  const value = Number(__ENV[name] ?? fallback);
  if (!Number.isSafeInteger(value) || value <= 0) {
    throw new Error(`${name} must be a positive integer`);
  }
  return value;
}

const baseURL = (__ENV.CALCSIDE_BASE_URL || 'http://127.0.0.1:8080').replace(/\/+$/, '');
const scenario = __ENV.BENCH_SCENARIO || 'exec';
const vus = positiveInteger('BENCH_VUS', 5);
const iterations = __ENV.BENCH_ITERATIONS === undefined ? null : positiveInteger('BENCH_ITERATIONS');
const duration = __ENV.BENCH_DURATION || '30s';
const requestTimeout = __ENV.BENCH_REQUEST_TIMEOUT || '30s';
const ttl = positiveInteger('BENCH_TTL_SECONDS', 900);
const headers = { 'Content-Type': 'application/json', 'X-Requested-With': 'calcside' };
if (__ENV.CALCSIDE_API_KEY) headers.Authorization = `Bearer ${__ENV.CALCSIDE_API_KEY}`;

const operations = {
  exec: ['exec'],
  lifecycle: ['createInstance', 'exec', 'deleteInstance'],
  read: [
    'me', 'capabilities', 'listInstances', 'getInstance', 'keepalive',
    'listFiles', 'readFile', 'inspect', 'prompt', 'listExecutions', 'getExecution', 'listAudit',
  ],
};
if (!Object.hasOwn(operations, scenario)) {
  throw new Error('BENCH_SCENARIO must be exec, lifecycle, or read');
}

const completed = new Counter('benchmark_iterations');
const success = new Rate('benchmark_success');
const execDuration = new Trend('benchmark_exec_duration', true);
const thresholds = {
  checks: ['rate==1'],
  benchmark_iterations: [iterations === null ? 'count>0' : `count==${vus * iterations}`],
  benchmark_success: ['rate==1'],
  'http_req_failed{phase:benchmark}': ['rate<0.01'],
  'http_reqs{phase:benchmark}': ['count>0'],
  'http_req_duration{phase:benchmark}': [],
};
for (const name of operations[scenario]) {
  thresholds[`http_req_duration{phase:benchmark,name:${name}}`] = __ENV.BENCH_P95_MS === undefined
    ? [] : [`p(95)<${positiveInteger('BENCH_P95_MS')}`];
}

export const options = {
  scenarios: {
    [scenario]: iterations === null
      ? { executor: 'constant-vus', vus, duration, gracefulStop: '35s' }
      : { executor: 'per-vu-iterations', vus, iterations, maxDuration: duration, gracefulStop: '35s' },
  },
  setupTimeout: __ENV.BENCH_SETUP_TIMEOUT || '2m',
  teardownTimeout: __ENV.BENCH_SETUP_TIMEOUT || '2m',
  systemTags: ['status', 'method', 'name', 'check', 'error_code', 'scenario', 'expected_response'],
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
  thresholds,
};

const code = `def sum_squares(n):
    total = 0
    for i in range(n):
        total += i * i
    return total
answer = sum_squares(1000)
fs.write("benchmark.txt", str(answer))
print(fs.read("benchmark.txt"))`;
const expected = '332833500';
const spec = {
  ttl_seconds: ttl,
  labels: { benchmark: 'k6', scenario },
  capabilities: { fs: {} },
};

function request(name, method, path, payload, status, validate, phase = 'benchmark') {
  const tags = { name, phase };
  const response = http.request(method, `${baseURL}${path}`, payload === null ? null : JSON.stringify(payload), {
    headers, tags, timeout: requestTimeout, redirects: 0, responseType: 'text',
    responseCallback: http.expectedStatuses(status),
  });
  let body = null;
  try {
    body = response.json();
  } catch {
    body = null;
  }
  const ok = check(response, {
    [`${name}: HTTP ${status}`]: (r) => r.status === status,
    [`${name}: valid result`]: () => body !== null && validate(body),
  }, tags);
  return { body, ok };
}

function instancePath(id) {
  return `/api/v1/instances/${encodeURIComponent(id)}`;
}

function createInstance(phase) {
  return request('createInstance', 'POST', '/api/v1/instances', spec, 201,
    (b) => typeof b.instance?.id === 'string' && b.instance.id.length > 0 && b.instance.status === 'running', phase);
}

function deleteInstance(id, phase) {
  return request('deleteInstance', 'DELETE', instancePath(id), null, 200, (b) => b.ok === true, phase);
}

function execute(id, phase = 'benchmark') {
  const result = request('exec', 'POST', `${instancePath(id)}/exec`, { code }, 200,
    (b) => b.error === null && b.output === `${expected}\n` && typeof b.exec_id === 'string'
      && b.exec_id.length > 0 && Number.isFinite(b.duration_ms) && b.duration_ms >= 0, phase);
  if (result.ok && phase === 'benchmark') execDuration.add(result.body.duration_ms);
  return result;
}

function cleanup(fixtures) {
  for (const fixture of fixtures) deleteInstance(fixture.id, 'teardown');
}

export function setup() {
  const auth = request('me', 'GET', '/api/v1/me', null, 200, (b) => typeof b.user?.id === 'string', 'setup');
  if (!auth.ok) fail('Preflight failed: check CALCSIDE_BASE_URL and CALCSIDE_API_KEY (or use a --dev server).');
  const fixtures = [];
  try {
    for (let i = 0; scenario !== 'lifecycle' && i < vus; i++) {
      const created = createInstance('setup');
      const id = created.body?.instance?.id;
      if (typeof id === 'string' && id) fixtures.push({ id });
      if (!created.ok) fail('Instance setup failed: check server capacity, policy, and --max-instances-per-user.');
      const seeded = execute(id, 'setup');
      if (!seeded.ok) fail('Workload setup failed: expected successful Starlark computation and fs read/write.');
      fixtures[fixtures.length - 1].execID = seeded.body.exec_id;
    }
    return fixtures;
  } catch (error) {
    cleanup(fixtures);
    throw error;
  }
}

function read(fixture) {
  const { id, execID } = fixture;
  const path = instancePath(id);
  const reads = [
    ['me', '/api/v1/me', (b) => typeof b.user?.id === 'string'],
    ['capabilities', '/api/v1/capabilities', (b) => Array.isArray(b.capabilities)],
    ['listInstances', '/api/v1/instances?status=running',
      (b) => Array.isArray(b.instances) && b.instances.some((item) => item.id === id)],
    ['getInstance', path, (b) => b.instance?.id === id && b.instance.status === 'running'],
    ['listFiles', `${path}/files?path=/work`,
      (b) => Array.isArray(b.entries) && b.entries.some((entry) => entry.name === 'benchmark.txt')],
    ['readFile', `${path}/files?path=/work/benchmark.txt`, (b) => b.content === expected],
    ['inspect', `${path}/inspect`, (b) => b.variables?.answer === expected],
    ['prompt', `${path}/prompt`, (b) => b.instance_id === id && typeof b.prompt === 'string' && b.prompt.length > 0],
    ['listExecutions', `${path}/executions?limit=10`,
      (b) => Array.isArray(b.executions) && b.executions.some((item) => item.id === execID && item.status === 'ok')],
    ['getExecution', `/api/v1/executions/${encodeURIComponent(execID)}`,
      (b) => b.execution?.id === execID && b.execution.status === 'ok' && b.code === code],
    ['listAudit', `/api/v1/audit?instance_id=${encodeURIComponent(id)}&limit=10`, (b) => Array.isArray(b.events)],
  ];
  let ok = request('keepalive', 'POST', `${path}/keepalive`, null, 200, (b) => b.instance?.id === id).ok;
  for (const [name, url, validate] of reads) {
    ok = request(name, 'GET', url, null, 200, validate).ok && ok;
  }
  return ok;
}

export default function (fixtures) {
  let ok = false;
  try {
    if (scenario === 'lifecycle') {
      const created = createInstance('benchmark');
      const id = created.body?.instance?.id;
      try {
        ok = created.ok && execute(id).ok;
      } finally {
        if (typeof id === 'string' && id) ok = deleteInstance(id, 'benchmark').ok && ok;
      }
    } else {
      const fixture = fixtures[execution.vu.idInTest - 1];
      if (!fixture) execution.test.abort('No fixture for this VU; use BENCH_VUS, not k6 --vus or distributed execution.');
      ok = scenario === 'exec' ? execute(fixture.id).ok : read(fixture);
    }
  } finally {
    success.add(ok);
    completed.add(1);
  }
}

export function teardown(fixtures) {
  cleanup(fixtures);
}
