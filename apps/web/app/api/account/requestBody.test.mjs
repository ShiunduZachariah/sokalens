import assert from "node:assert/strict";
import test from "node:test";
import { AccountBodyError, accountBodyLimit, readAccountBody } from "./requestBody.ts";

function request(body, headers = {}, signal) {
  return new Request("http://localhost/api/account/v1/auth/login", {
    method: "POST", body, headers, signal, duplex: "half",
  });
}
const status = (expected) => (error) => error instanceof AccountBodyError && error.status === expected;

test("empty, JSON and exactly 64 KiB bodies retain their bytes", async () => {
  for (const input of [new Uint8Array(), new TextEncoder().encode('{"name":"José"}'), new Uint8Array(accountBodyLimit).fill(97)]) {
    assert.deepEqual(new Uint8Array(await readAccountBody(request(input))), input);
  }
  assert.equal((await readAccountBody(request(undefined))).byteLength, 0);
});

test("declared oversize and malformed lengths fail before consuming a stream", async () => {
  for (const [length, expected] of [[String(accountBodyLimit + 1), 413], ["9999999999999999999999", 413], ["invalid", 400]]) {
    let cancelled = false;
    const stream = new ReadableStream({ cancel() { cancelled = true; } });
    await assert.rejects(readAccountBody(request(stream, { "content-length": length })), status(expected));
    assert.equal(cancelled, true);
  }
});

test("actual byte limit rejects chunked bodies even with a false small length", async () => {
  for (const headers of [{}, { "content-length": "1" }]) {
    let cancelled = false;
    const stream = new ReadableStream({
      start(controller) {
        controller.enqueue(new Uint8Array(accountBodyLimit));
        controller.enqueue(new Uint8Array(1));
      },
      cancel() { cancelled = true; },
    });
    await assert.rejects(readAccountBody(request(stream, headers)), status(413));
    assert.equal(cancelled, true);
  }
});

test("limit counts UTF-8 bytes instead of characters", async () => {
  await assert.rejects(readAccountBody(request("é".repeat(accountBodyLimit / 2 + 1))), status(413));
});

test("stalled uploads time out without waiting for stalled cancellation", async () => {
  let cancelled = false;
  const stream = new ReadableStream({ cancel() { cancelled = true; return new Promise(() => {}); } });
  await assert.rejects(readAccountBody(request(stream), 20), status(408));
  assert.equal(cancelled, true);
});

test("client disconnect cancels a pending upload", async () => {
  const controller = new AbortController();
  let cancelled = false;
  const stream = new ReadableStream({ cancel() { cancelled = true; } });
  const reading = readAccountBody(request(stream, {}, controller.signal));
  controller.abort();
  await assert.rejects(reading, status(400));
  assert.equal(cancelled, true);
});
