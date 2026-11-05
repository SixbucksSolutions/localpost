import test from "node:test";
import assert from "node:assert/strict";
import { MAX_ACTIONS, COUNTER_STEP, createCounter, diagnostic, endpoint, mount, sendDiagnostic } from "./pilot.mjs";

const runId = "22222222-2222-4222-8222-222222222222";
const config = { enabled: true, base: "http://127.0.0.1:53147", registrationId: runId };

test("only loopback HTTP roots and UUIDv4 registrations are accepted", () => {
  assert.equal(endpoint(config.base, runId), `${config.base}/api/2026-11-05/registrations/${runId}/messages`);
  assert.equal(endpoint("http://[::1]:53147/", runId), `http://[::1]:53147/api/2026-11-05/registrations/${runId}/messages`);
  for (const base of ["https://127.0.0.1", "http://example.org", "http://localhost", "http://127.0.0.1/path", "http://user@127.0.0.1", "http://127.0.0.1/?x=1", "http://127.0.0.1/#x"]) {
    assert.throws(() => endpoint(base, runId));
  }
  assert.throws(() => endpoint(config.base, "22222222-2222-1222-8222-222222222222"));
});

test("schema is bounded and diagnoses the controlled wrong-delta fault", () => {
  assert.equal(COUNTER_STEP, 1);
  assert.deepEqual(diagnostic(runId, 1, 2), {
    schemaVersion: 1, runId, correlationId: `${runId}:1`, actionSequence: 1,
    operation: "add_one", expectedCount: 1, observedCount: 2, status: "mismatch",
  });
  for (const values of [[runId, 0, 0], [runId, MAX_ACTIONS + 1, 1], [runId, 1, Infinity], [runId, 1, 2001], ["secret", 1, 1]]) {
    assert.throws(() => diagnostic(...values));
  }
  const faulty = createCounter(runId, undefined, 2).addOne();
  assert.equal(faulty.record.status, "mismatch");
});

test("sender posts only the exact allowlisted JSON body and cleans receipt stream", async () => {
  let calls = 0;
  let cancelled = false;
  let signal;
  const record = { ...diagnostic(runId, 1, 1), secret: "must not send" };
  assert.equal(await sendDiagnostic(config, record, async (url, options) => {
    calls += 1;
    assert.equal(url, endpoint(config.base, runId));
    assert.equal(options.method, "POST");
    assert.deepEqual(options.headers, { "Content-Type": "application/json" });
    assert.equal(options.credentials, "omit");
    assert.equal(options.redirect, "error");
    assert.equal(options.body, JSON.stringify(diagnostic(runId, 1, 1)));
    signal = options.signal;
    return { status: 200, body: { cancel: async () => { cancelled = true; } } };
  }), "sent");
  assert.equal(calls, 1);
  assert.equal(cancelled, true);
  assert.equal(signal.aborted, false);
});

test("disabled and invalid config do not call transport; rejection does not retry", async () => {
  let calls = 0;
  const fail = () => { calls += 1; throw new Error("private provider text"); };
  const record = diagnostic(runId, 1, 1);
  assert.equal(await sendDiagnostic({ enabled: false }, record, fail), "disabled");
  assert.equal(await sendDiagnostic({ ...config, base: "http://remote.test" }, record, fail), "invalid_config");
  assert.equal(calls, 0);
  assert.equal(await sendDiagnostic(config, record, fail), "network_error");
  assert.equal(calls, 1);
  assert.equal(await sendDiagnostic(config, record, async () => ({ status: 503 })), "http_error");
});

test("timeout aborts once and settles even if transport ignores abort", async () => {
  let signal;
  let calls = 0;
  const status = await sendDiagnostic(config, diagnostic(runId, 1, 1), (_url, options) => {
    calls += 1;
    signal = options.signal;
    return new Promise(() => {});
  }, 5);
  assert.equal(status, "timeout");
  assert.equal(signal.aborted, true);
  assert.equal(calls, 1);
});

test("counter commits once before logging and contains rejected or throwing senders", async () => {
  let calls = 0;
  const counter = createCounter(runId, () => { calls += 1; throw new Error("private"); });
  const first = counter.addOne();
  assert.equal(first.count, 1);
  assert.equal(calls, 0);
  assert.equal(await first.delivery, "network_error");
  const second = counter.addOne();
  assert.equal(second.count, 2);
  assert.equal(await second.delivery, "network_error");
  assert.equal(calls, 2);
  assert.equal(counter.isLatest(first.sequence), false);
  assert.equal(counter.isLatest(second.sequence), true);
  const rejected = createCounter(runId, () => Promise.reject(new Error("private")));
  assert.equal(await rejected.addOne().delivery, "network_error");
});

test("counter bounds the run without sending extra actions", async () => {
  const counter = createCounter(runId);
  let last;
  for (let n = 0; n < MAX_ACTIONS; n += 1) last = counter.addOne();
  assert.equal(last.count, MAX_ACTIONS);
  assert.equal(await last.delivery, "disabled");
  assert.equal(counter.addOne(), null);
});

test("view ignores an older diagnostic result after a newer action", async () => {
  const elements = Object.fromEntries(["run", "enabled", "base", "registration", "count", "sequence", "delivery", "add"].map(id => [id, {}]));
  elements.enabled.checked = true;
  elements.base.value = config.base;
  elements.registration.value = runId;
  let click;
  elements.add.addEventListener = (_event, listener) => { click = listener; };
  const originalFetch = globalThis.fetch;
  const requests = [];
  globalThis.fetch = () => new Promise(resolve => requests.push(resolve));
  try {
    mount({ getElementById: id => elements[id] }, { randomUUID: () => runId });
    click();
    assert.equal(elements.count.textContent, "1");
    assert.equal(requests.length, 0);
    click();
    await new Promise(resolve => setImmediate(resolve));
    requests[1]({ status: 200 });
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(elements.delivery.textContent, "sent");
    requests[0]({ status: 503 });
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(elements.delivery.textContent, "sent");
    assert.equal(elements.count.textContent, "2");
  } finally {
    globalThis.fetch = originalFetch;
    for (const resolve of requests) resolve({ status: 200 });
  }
});
