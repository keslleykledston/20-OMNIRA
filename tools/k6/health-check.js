import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';

// Custom metrics
const errorRate = new Rate('errors');
const healthCheckDuration = new Trend('health_check_duration');
const healthCheckStatus = new Rate('health_check_status');

export const options = {
  stages: [
    { duration: '30s', target: 10 },    // Ramp-up to 10 VUs
    { duration: '60s', target: 50 },    // Ramp-up to 50 VUs
    { duration: '30s', target: 50 },    // Stay at 50 VUs
    { duration: '30s', target: 0 },     // Ramp-down to 0 VUs
  ],
  thresholds: {
    'http_req_duration': ['p(95)<500'],  // 95% of requests under 500ms
    'http_req_failed': ['rate<0.1'],     // Error rate < 10%
  },
};

export default function () {
  const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

  // Test /healthz endpoint
  const healthRes = http.get(`${BASE_URL}/healthz`);

  healthCheckDuration.add(healthRes.timings.duration);

  const statusOk = check(healthRes, {
    'status is 200': (r) => r.status === 200,
    'response has status field': (r) => r.json('status') !== undefined,
    'database is healthy': (r) => r.json('components.database.status') === 'healthy',
    'nats is healthy': (r) => r.json('components.nats.status') === 'healthy',
  });

  if (!statusOk) {
    errorRate.add(1);
  } else {
    errorRate.add(0);
  }

  healthCheckStatus.add(statusOk);

  sleep(1);
}
