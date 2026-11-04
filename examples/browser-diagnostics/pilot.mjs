export const MAX_ACTIONS = 1000;
const UUID_V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

// Controlled fault point: M04.02 may temporarily change 1 to 2, then restore it.
export const COUNTER_STEP = 1;

export function endpoint(base, registrationId) {
  if (typeof base !== "string" || base.length > 80 ||
      typeof registrationId !== "string" || !UUID_V4.test(registrationId)) {
    throw new Error("invalid_config");
  }
  const url = new URL(base);
  if (url.protocol !== "http:" || !["127.0.0.1", "[::1]"].includes(url.hostname) ||
      url.username || url.password || url.pathname !== "/" || url.search || url.hash) {
    throw new Error("invalid_config");
  }
  return `${url.origin}/registrations/${registrationId}/messages`;
}

export function diagnostic(runId, sequence, observedCount) {
  if (typeof runId !== "string" || !UUID_V4.test(runId) ||
      !Number.isInteger(sequence) || sequence < 1 || sequence > MAX_ACTIONS ||
      !Number.isInteger(observedCount) || observedCount < 0 || observedCount > MAX_ACTIONS * 2) {
    throw new Error("invalid_diagnostic");
  }
  // Construct an allowlisted record, never copy browser/provider objects.
  return {
    schemaVersion: 1, runId, correlationId: `${runId}:${sequence}`,
    actionSequence: sequence, operation: "add_one", expectedCount: sequence,
    observedCount, status: observedCount === sequence ? "match" : "mismatch",
  };
}

export async function sendDiagnostic(config, record, fetcher = globalThis.fetch, timeoutMs = 1500) {
  if (!config.enabled) return "disabled";
  let target;
  let body;
  try {
    target = endpoint(config.base, config.registrationId);
    body = JSON.stringify(diagnostic(record.runId, record.actionSequence, record.observedCount));
    if (!Number.isInteger(timeoutMs) || timeoutMs < 1 || timeoutMs > 10000) return "invalid_config";
  } catch {
    return "invalid_config";
  }
  const controller = new AbortController();
  let timer;
  const timeout = new Promise(resolve => {
    timer = setTimeout(() => { controller.abort(); resolve("timeout"); }, timeoutMs);
  });
  const request = Promise.resolve().then(async () => {
    try {
      const response = await fetcher(target, {
        method: "POST", headers: { "Content-Type": "application/json" },
        credentials: "omit", redirect: "error", cache: "no-store",
        body, signal: controller.signal,
      });
      // Receipt contents are unnecessary; release the stream without parsing it.
      if (response.body) await response.body.cancel();
      return response.status === 200 ? "sent" : "http_error";
    } catch {
      return controller.signal.aborted ? "timeout" : "network_error";
    }
  });
  try {
    return await Promise.race([request, timeout]);
  } finally {
    clearTimeout(timer);
  }
}

export function createCounter(runId, sender = () => Promise.resolve("disabled"), step = COUNTER_STEP) {
  if (typeof runId !== "string" || !UUID_V4.test(runId) || ![1, 2].includes(step)) throw new Error("invalid_counter");
  let sequence = 0;
  let count = 0;
  return {
    isLatest: action => action === sequence,
    addOne() {
      if (sequence === MAX_ACTIONS) return null;
      sequence += 1;
      count += step;
      const record = diagnostic(runId, sequence, count);
      // Invoke outside the action stack and contain synchronous and async failures.
      const delivery = Promise.resolve().then(() => sender(record)).catch(() => "network_error");
      return { count, sequence, record, delivery };
    },
  };
}

export function mount(document, crypto) {
  const runId = crypto.randomUUID();
  const byId = id => document.getElementById(id);
  byId("run").textContent = runId;
  const counter = createCounter(runId, record => sendDiagnostic({
    enabled: byId("enabled").checked, base: byId("base").value.trim(),
    registrationId: byId("registration").value.trim(),
  }, record));
  byId("add").addEventListener("click", () => {
    const action = counter.addOne();
    if (!action) return;
    byId("count").textContent = String(action.count);
    byId("sequence").textContent = String(action.sequence);
    byId("delivery").textContent = "pending";
    byId("add").disabled = action.sequence === MAX_ACTIONS;
    void action.delivery.then(status => {
      if (counter.isLatest(action.sequence)) byId("delivery").textContent = status;
    });
  });
}

if (typeof document !== "undefined") mount(document, globalThis.crypto);
