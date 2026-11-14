import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  stages: [
    { duration: '10s', target: 50 },    // Ramp to 50 VUs
    { duration: '30s', target: 50 },    // Hold 30s
    { duration: '10s', target: 0 },     // Ramp down
  ],
  thresholds: {
    http_req_failed: ['rate<0.1'],
  },
};

export default function () {
  const baseURL = 'http://localhost:8080';

  // 70% reads, 30% writes (realistic flash sale)
  if (Math.random() < 0.7) {
    // Read product stock
    const res = http.get(`${baseURL}/product?id=1`);
    check(res, { 'read ok': r => r.status === 200 });
  } else {
    // Try to buy product 1 (hot SKU - creates lock contention)
    const res = http.get(`${baseURL}/order?id=1`);
    check(res, { 'order ok': r => r.status === 200 || r.status === 409 });
  }

  sleep(0.05); // 20 req/sec per user
}
