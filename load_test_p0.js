import http from 'k6/http';
import { check, sleep, group } from 'k6';

export const options = {
  stages: [
    { duration: '30s', target: 100 },   // Ramp up
    { duration: '1m', target: 100 },    // Stay at 100
    { duration: '30s', target: 0 },     // Ramp down
  ],
  thresholds: {
    http_req_duration: ['p(95)<500', 'p(99)<1000'],
    http_req_failed: ['rate<0.1'],
  },
};

export default function () {
  const baseURL = 'http://localhost:8080';

  group('Catalog Reads', () => {
    const productId = Math.floor(Math.random() * 10) + 1;
    const res = http.get(`${baseURL}/catalog/product?id=${productId}`);
    check(res, {
      'status is 200': (r) => r.status === 200,
      'response time < 100ms': (r) => r.timings.duration < 100,
    });
  });

  sleep(0.1);

  group('Create Orders', () => {
    const productId = Math.floor(Math.random() * 10) + 1;
    const userId = Math.floor(Math.random() * 100000);
    const payload = JSON.stringify({
      user_id: userId,
      product_id: productId,
      quantity: 1,
    });

    const res = http.post(`${baseURL}/orders`, payload, {
      headers: { 'Content-Type': 'application/json' },
    });

    check(res, {
      'status is 200': (r) => r.status === 200 || r.status === 409,
      'response time < 500ms': (r) => r.timings.duration < 500,
    });
  });

  sleep(0.1);

  group('Metrics', () => {
    const res = http.get(`${baseURL}/metrics`);
    check(res, {
      'metrics available': (r) => r.status === 200,
    });
  });
}
