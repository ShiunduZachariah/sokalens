import assert from "node:assert/strict";
import test from "node:test";

const { nullBodyStatus, nullBodyStatuses } = await import("./nullBodyStatus.ts");

test("204 is a null-body status", () => {
  // DELETE /v1/me/favorites answers 204. Removing a favorite is the only
  // DELETE the web app issues, so this one status decides whether unsaving
  // reports success or failure to the user.
  assert.equal(nullBodyStatus(204), true);
  assert.equal(nullBodyStatus(205), true);
  assert.equal(nullBodyStatus(304), true);
});

test("ordinary statuses still carry their body", () => {
  for (const status of [200, 201, 400, 401, 404, 409, 500, 503]) {
    assert.equal(nullBodyStatus(status), false, `status ${status}`);
  }
});

test("attaching a body to a null-body status really does throw", () => {
  // This is the defect the helper exists to avoid: the proxy built every
  // upstream response as `new NextResponse(await upstream.arrayBuffer(),
  // { status })`, the constructor threw on 204, and the surrounding catch
  // reported "Account services are temporarily unavailable" -- a 503 -- even
  // though the favorite had in fact been deleted.
  assert.throws(() => new Response(new ArrayBuffer(0), { status: 204 }), TypeError);
});

test("passing null instead lets the same status through", () => {
  const response = new Response(null, { status: 204 });
  assert.equal(response.status, 204);
});

test("every listed status is genuinely body-less to the runtime", () => {
  for (const status of nullBodyStatuses) {
    // 101 and 103 are not constructible as a Response at all; the rest must
    // survive with a null body and reject a non-null one.
    if (status < 200) continue;
    assert.doesNotThrow(() => new Response(null, { status }), `null body at ${status}`);
    assert.throws(() => new Response(new ArrayBuffer(0), { status }), TypeError, `empty buffer at ${status}`);
  }
});
