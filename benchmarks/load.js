import http from 'k6/http';
import { check } from 'k6';
const duration = Number(__ENV.DURATION || 30);
const rate = Number(__ENV.RATE || 900);
const maxVUs = Number(__ENV.MAX_VUS || 1000);
const common = {startTime: '0s', preAllocatedVUs: Math.min(200, maxVUs), maxVUs};
const steady = {...common, executor: 'constant-arrival-rate', rate, timeUnit: '1s', duration: `${duration}s`};
const spike = {...common, executor: 'ramping-arrival-rate', startRate: rate, timeUnit: '1s', stages: [
  {target: rate, duration: `${duration/3}s`},
  {target: rate*3, duration: `${duration/3}s`},
  {target: rate, duration: `${duration/3}s`},
]};
export const options = {
  scenarios: {load: __ENV.SCENARIO === 'traffic_spike' ? spike : steady},
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'min', 'max'],
  // Preserve failed experiments; analysis reports errors and dropped arrivals.
  // Do not silently discard slower algorithms with an arbitrary threshold.
  discardResponseBodies: true,
  systemTags: ['status', 'method', 'scenario', 'expected_response'],
};
export default function () {
  const res = http.get(__ENV.TARGET || 'http://127.0.0.1:8080/', {timeout: '5s'});
  check(res, {'status 200': r => r.status === 200});
}
export function handleSummary(data) {
  const m = data.metrics;
  const latency = m.http_req_duration.values;
  const message = `Requests: ${m.http_reqs.values.count}; RPS: ${m.http_reqs.values.rate.toFixed(2)}; P95: ${latency['p(95)'].toFixed(2)} ms; P99: ${latency['p(99)'].toFixed(2)} ms; error fraction: ${m.http_req_failed.values.rate.toFixed(4)}; dropped: ${m.dropped_iterations ? m.dropped_iterations.values.count : 0}\n`;
  return {[__ENV.SUMMARY || 'summary.json']: JSON.stringify(data, null, 2), stdout: message};
}
