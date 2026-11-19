import http from 'k6/http';
import { check, sleep, group } from 'k6';

export const options = {
  stages: [
    { duration: '10s', target: 200 },    // Ramp to 200 VUs
    { duration: '30s', target: 200 },    // Hold 30s (measure baseline + cache hits)
    { duration: '10s', target: 0 },      // Ramp down
  ],
  thresholds: {
    'http_req_duration{type:read,cache:hit}': ['p(95)<50'],     // Cache hits should be <50ms
    'http_req_duration{type:read,cache:miss}': ['p(95)<200'],   // Cache misses: query replica
    'http_req_duration{type:write}': ['p(95)<1000'],
  },
};

export default function () {
  const baseURL = 'http://localhost:8080';

  // 70% reads, 30% writes (same as P0)
  if (Math.random() < 0.7) {
    // Read product stock
    const cacheStatus = Math.random() < 0.5 ? 'hit' : 'miss';
    const res = http.get(`${baseURL}/product?id=1`, {
      tags: { type: 'read', cache: cacheStatus },
    });

    check(res, {
      'read ok': r => r.status === 200,
      'cache tagged': r => r.headers['X-Cache'] !== undefined,
    });
  } else {
    // Try to buy product 1 (hot SKU - creates lock contention)
    const res = http.get(`${baseURL}/order?id=1`, {
      tags: { type: 'write' },
    });
    check(res, { 'write ok': r => r.status === 200 || r.status === 409 });
  }

  sleep(0.05);
}

// Scenario 2: Cache stampede detection (run this separately)
export function stampede() {
  const baseURL = 'http://localhost:8080';

  group('cache stampede scenario', function () {
    // Warm cache
    for (let i = 0; i < 10; i++) {
      http.get(`${baseURL}/product?id=1`, { tags: { type: 'read' } });
    }

    sleep(1);

    // Now 200 VUs hit simultaneously - should be coalesced
    for (let i = 0; i < 200; i++) {
      http.get(`${baseURL}/product?id=1`, {
        tags: { type: 'read', scenario: 'stampede' },
      });
    }
  });
}
