import http from "k6/http";
import { check } from "k6";

export default function () {
  let params = { timeout: 30 };
  let res = http.get(`${__ENV.FN_ENDPOINT}`);
  check(res, {
    "status is 200": (r) => r.status === 200,
  });
}
