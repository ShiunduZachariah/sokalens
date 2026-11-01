import test from "node:test";
import assert from "node:assert/strict";

import { fetchPredictionApi, predictionApiBaseUrl } from "./apiClient.ts";

test("prediction API client trims the configured base URL", () => {
  const previous = process.env.PREDICTION_API_BASE_URL;
  process.env.PREDICTION_API_BASE_URL = "http://api.internal:8080/";
  try {
    assert.equal(predictionApiBaseUrl(), "http://api.internal:8080");
  } finally {
    if (previous === undefined) delete process.env.PREDICTION_API_BASE_URL;
    else process.env.PREDICTION_API_BASE_URL = previous;
  }
});

test("prediction API client applies revalidation and a request deadline", async () => {
  const previousBaseUrl = process.env.PREDICTION_API_BASE_URL;
  const previousFetch = globalThis.fetch;
  process.env.PREDICTION_API_BASE_URL = "http://api.internal:8080";
  let request;

  globalThis.fetch = async (input, init) => {
    request = { input, init };
    return new Response("{}", { status: 200 });
  };

  try {
    await fetchPredictionApi("v1/predictions/today?limit=6", { revalidate: 60 });
    assert.equal(request.input, "http://api.internal:8080/v1/predictions/today?limit=6");
    assert.equal(request.init.headers.Accept, "application/json");
    assert.equal(request.init.next.revalidate, 60);
    assert.ok(request.init.signal instanceof AbortSignal);
  } finally {
    globalThis.fetch = previousFetch;
    if (previousBaseUrl === undefined) delete process.env.PREDICTION_API_BASE_URL;
    else process.env.PREDICTION_API_BASE_URL = previousBaseUrl;
  }
});
