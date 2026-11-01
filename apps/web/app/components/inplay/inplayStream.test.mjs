import assert from "node:assert/strict";
import { readFileSync, existsSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import test from "node:test";
import ts from "typescript";

const require = createRequire(import.meta.url);
function load(filename, dependencies) {
  const compiled = ts.transpileModule(readFileSync(filename, "utf8"), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  const module = { exports: {} };
  new Function("require", "module", "exports", compiled)((name) => {
    if (name in dependencies) return dependencies[name];
    if (!name.startsWith(".")) return require(name);
    const base = path.resolve(path.dirname(filename), name);
    return load([base, `${base}.ts`, `${base}.tsx`].find(existsSync), dependencies);
  }, module, module.exports);
  return module.exports;
}

function client(t, failApply = false) {
  const state = [{ cursor: 10, fixture: null, snapshot: null }, "ready", "stream"];
  const cursor = { current: 10 };
  const effects = [];
  const intervals = [];
  let index = 0;
  let source;
  class Source {
    handlers = new Map();
    closed = false;
    constructor(url) { this.url = url; source = this; }
    addEventListener(name, callback) { this.handlers.set(name, callback); }
    close() { this.closed = true; }
    emit(name, data, lastEventId = "") { this.handlers.get(name)?.({ data, lastEventId }); }
  }
  const previousWindow = globalThis.window;
  const previousSource = globalThis.EventSource;
  globalThis.window = { EventSource: Source, setInterval: (callback) => { intervals.push(callback); return intervals.length; }, clearInterval: () => {} };
  globalThis.EventSource = Source;
  t.after(() => { globalThis.window = previousWindow; globalThis.EventSource = previousSource; });
  const { InPlayMarkets } = load(path.join(import.meta.dirname, "InPlayMarkets.tsx"), {
    react: {
      useState: () => { const i = index++; return [state[i], (value) => { if (failApply && i === 0) throw new Error("Apply failed"); state[i] = typeof value === "function" ? value(state[i]) : value; }]; },
      useRef: () => cursor,
      useCallback: (fn) => fn,
      useMemo: (fn) => fn(),
      useEffect: (fn) => effects.push(fn),
    },
  });
  function render() { index = 0; effects.length = 0; InPlayMarkets({ fixtureId: 1, phase: "live" }); }
  render();
  const cleanup = effects[1]();
  return {
    state, cursor, cleanup, get source() { return source; },
    poll() { render(); return effects[3](); },
    refresh() { render(); effects[2](); intervals.at(-1)(); },
  };
}

const frame = (cursor) => JSON.stringify({ cursor, snapshots: [{ fixture_id: 1, sequence: cursor, model_health: "ok" }], events: [], more: false });

test("named changes apply; duplicate or delayed frames cannot rewind the applied cursor", (t) => {
  const c = client(t);
  c.source.emit("changes", frame(20), "20");
  assert.equal(c.cursor.current, 20);
  assert.equal(c.state[0].snapshot.sequence, 20);
  c.source.emit("changes", frame(15), "15");
  assert.equal(c.cursor.current, 20);
  assert.equal(c.state[0].snapshot.sequence, 20);
});

test("an open match page replaces its market set with the current fixture publication", async (t) => {
  const c = client(t);
  t.mock.method(globalThis, "fetch", async () => Response.json({
    fixture: { fixture_id: 1, markets: [{ key: "next_goal", prediction: { selection: "away", probability_bp: 6200 } }] },
  }));
  c.refresh();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(c.state[0].fixture.markets[0].prediction.selection, "away");
});

for (const data of ["broken JSON", "{}", '{"cursor":30,"snapshots":[{}],"events":[]}', '{"cursor":30,"snapshots":[],"events":{}}', '{"cursor":"30","snapshots":[],"events":[]}']) {
  test(`malformed changes close the stream without acknowledging unapplied data: ${data}`, (t) => {
    const c = client(t);
    c.source.emit("changes", frame(20), "20");
    c.source.emit("changes", data, "30");
    assert.equal(c.source.closed, true);
    assert.equal(c.state[2], "poll");
    assert.equal(c.cursor.current, 20);
    c.source.emit("changes", frame(50), "50");
    assert.equal(c.cursor.current, 20, "queued frames from a closed source must be ignored");
    c.source.emit("reconnect", '{"cursor":40}', "40");
    assert.equal(c.cursor.current, 20);
  });
}

test("a deliberate reconnect falls back from the applied cursor, never the advertised cursor", async (t) => {
  const c = client(t);
  c.source.emit("changes", frame(20), "20");
  c.source.emit("reconnect", '{"cursor":40}', "40");
  assert.equal(c.cursor.current, 20);
  assert.equal(c.source.closed, true);
  const requests = [];
  t.mock.method(globalThis, "fetch", async (url) => { requests.push(url); return new Response(frame(21), { headers: { "Content-Type": "application/json" } }); });
  c.poll();
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(requests, ["/api/account/v1/inplay/changes?since=20"]);
  assert.equal(c.cursor.current, 21);
});

test("Go's null empty event list is a valid frame", (t) => {
  const c = client(t);
  c.source.emit("changes", JSON.stringify({ cursor: 20, snapshots: [], events: null, more: false }), "20");
  assert.equal(c.cursor.current, 20);
  assert.equal(c.source.closed, false);
});

test("a failed apply cannot acknowledge the frame", (t) => {
  const c = client(t, true);
  c.source.emit("changes", frame(20), "20");
  assert.equal(c.cursor.current, 10);
  assert.equal(c.state[2], "poll");
  assert.equal(c.source.closed, true);
});

test("cleanup prevents queued frames from an obsolete stream applying", (t) => {
  const c = client(t);
  c.cleanup();
  c.source.emit("changes", frame(20), "20");
  c.source.emit("reconnect", '{"cursor":40}', "40");
  assert.equal(c.cursor.current, 10);
  assert.equal(c.state[2], "stream");
});

test("an empty or older snapshot keeps the standing snapshot while cursor advances", (t) => {
  const c = client(t);
  c.source.emit("changes", frame(20), "20");
  c.source.emit("changes", '{"cursor":30,"snapshots":[],"events":[],"more":false}', "30");
  assert.equal(c.state[0].snapshot.sequence, 20);
  const older = JSON.parse(frame(10));
  older.cursor = 40;
  c.source.emit("changes", JSON.stringify(older), "40");
  assert.equal(c.cursor.current, 40);
  assert.equal(c.state[0].snapshot.sequence, 20);
});

test("a mismatched SSE id cannot cause native resume beyond applied data", (t) => {
  const c = client(t);
  c.source.emit("changes", frame(20), "30");
  assert.equal(c.cursor.current, 10);
  assert.equal(c.source.closed, true);
});

test("a transport drop after a valid frame keeps native Last-Event-ID reconnect", (t) => {
  const c = client(t);
  c.source.emit("changes", frame(20), "20");
  c.source.emit("error");
  assert.equal(c.source.closed, false);
  assert.equal(c.state[2], "stream");
  assert.equal(c.cursor.current, 20);
});

test("a stream failure before any frame falls back to polling", (t) => {
  const c = client(t);
  c.source.emit("error");
  assert.equal(c.source.closed, true);
  assert.equal(c.state[2], "poll");
});
