import http from 'k6/http';
import { check, sleep, group } from 'k6';
import { Rate, Trend } from 'k6/metrics';

// Custom metrics
const getTenantDuration = new Trend('get_tenant_duration');
const getTenantStatus = new Rate('get_tenant_status');
const listMembershipsDuration = new Trend('list_memberships_duration');
const listMembershipsStatus = new Rate('list_memberships_status');
const errorRate = new Rate('errors');

// Mock JWT token (replace with real token for integration tests)
const MOCK_JWT = 'eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiI1NTBlODQwMC1lMjliLTQxZDQtYTcxNi00NDY2NTU0NDAwMDAiLCJpYXQiOjE2MjYxODk2OTcsImV4cCI6OTk5OTk5OTk5OX0.test';

export const options = {
  stages: [
    { duration: '20s', target: 5 },     // Ramp-up to 5 VUs
    { duration: '40s', target: 20 },    // Ramp-up to 20 VUs
    { duration: '30s', target: 20 },    // Stay at 20 VUs
    { duration: '20s', target: 0 },     // Ramp-down
  ],
  thresholds: {
    'http_req_duration': ['p(95)<1000', 'p(99)<2000'],
    'http_req_failed': ['rate<0.05'],
  },
};

export default function () {
  const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
  const headers = {
    'Authorization': `Bearer ${MOCK_JWT}`,
    'Content-Type': 'application/json',
  };

  group('Tenant Endpoints', () => {
    // GET /tenant
    const tenantRes = http.get(`${BASE_URL}/tenant`, { headers });

    getTenantDuration.add(tenantRes.timings.duration);

    const tenantOk = check(tenantRes, {
      'GET /tenant status is 200 or 401': (r) => r.status === 200 || r.status === 401,
      'response is JSON': (r) => r.headers['Content-Type'].includes('application/json'),
    });

    if (!tenantOk) {
      errorRate.add(1);
    }

    getTenantStatus.add(tenantOk);

    sleep(0.5);

    // GET /tenant/memberships
    const membershipsRes = http.get(`${BASE_URL}/tenant/memberships`, { headers });

    listMembershipsDuration.add(membershipsRes.timings.duration);

    const membershipsOk = check(membershipsRes, {
      'GET /tenant/memberships status is 200 or 401': (r) => r.status === 200 || r.status === 401,
      'response is JSON': (r) => r.headers['Content-Type'].includes('application/json'),
    });

    if (!membershipsOk) {
      errorRate.add(1);
    }

    listMembershipsStatus.add(membershipsOk);

    sleep(1);
  });
}
