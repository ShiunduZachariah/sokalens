import http from "k6/http";
import { check, sleep } from "k6";

export const options = {
  vus: 5,
  duration: "15s",
  thresholds: {
    http_req_failed: ["rate<0.01"],
    http_req_duration: ["p(95)<500"],
  },
};

const baseURL = __ENV.API_BASE_URL || "http://localhost:8080";

export default function () {
  const health = http.get(`${baseURL}/health`);
  check(health, {
    "health alias is 200": (response) => response.status === 200,
  });

  const ready = http.get(`${baseURL}/ready`);
  check(ready, {
    "ready alias is 200": (response) => response.status === 200,
  });

  sleep(1);
}
