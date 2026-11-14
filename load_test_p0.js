import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  stages: [
    { duration: '10s', target: 200 },    // Ramp to 50 VUs
    { duration: '30s', target: 200 },    // Hold 30s
    { duration: '10s', target: 0 },     // Ramp down
  ],
  thresholds: {
    'http_req_duration{type:read}': ['p(95)<100'],
    'http_req_duration{type:write}': ['p(95)<1000'],
  },
};

export default function () {
  const baseURL = 'http://localhost:8080';

  // 70% reads, 30% writes (realistic flash sale)
  if (Math.random() < 0.7) {
    // Read product stock
    const res = http.get(`${baseURL}/product?id=1`, {
      tags: { type: 'read' },
    });
    check(res, { 'read ok': r => r.status === 200 });
  } else {
    // Try to buy product 1 (hot SKU - creates lock contention)
    const res = http.get(`${baseURL}/order?id=1`, {
      tags: { type: 'write' },
    });
    check(res, { 'write ok': r => r.status === 200 || r.status === 409 });
  }

  sleep(0.05);
}
