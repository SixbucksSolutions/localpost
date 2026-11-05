// Bounded real-browser proof using the shared file-check helper and installed Chrome.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { mkdir, readFile, readdir, realpath, writeFile } from "node:fs/promises";
import path from "node:path";
import { parseArgs } from "node:util";
import { fileURLToPath } from "node:url";
import { diagnostic, endpoint } from "./pilot.mjs";
import { readRegularFileBounded } from "../../scripts/check-openapi-service.mjs";

const { values } = parseArgs({ options: Object.fromEntries(
  ["service-url", "pilot-url", "mailbox-root", "evidence-directory", "chrome"].map(key => [key, { type: "string" }]),
) });
for (const key of ["service-url", "pilot-url", "mailbox-root", "evidence-directory", "chrome"]) {
  assert.ok(values[key], `missing --${key}`);
}
const service = new URL(values["service-url"]);
const pilot = new URL(values["pilot-url"]);
const placeholderID = "22222222-2222-4222-8222-222222222222";
endpoint(service.href, placeholderID);
endpoint(pilot.href, placeholderID);
const root = await realpath(values["mailbox-root"]);
const evidence = path.resolve(values["evidence-directory"]);
const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
for (const directory of [root, evidence]) {
  const relative = path.relative(repository, directory);
  assert.ok(relative.startsWith(`..${path.sep}`) || path.isAbsolute(relative), "private directories must be outside the repository");
}
// Exclusive directory creation prevents overwriting evidence from an earlier run.
await mkdir(evidence, { mode: 0o700 });
const profile = path.join(evidence, "chrome-profile");
await mkdir(profile, { mode: 0o700 });
const api = `${service.origin}/api/2026-11-05`;
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(check, label, timeout = 10000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const result = await check();
    if (result) return result;
    await sleep(100);
  }
  throw new Error(`timeout: ${label}`);
}
async function request(url, options = {}) {
  return fetch(url, { ...options, signal: AbortSignal.timeout(5000) });
}
let registration;
let browser;
let socket;
let retired = false;
let nextID = 0;
const pending = new Map();
const network = [];
function command(method, params = {}) {
  const id = ++nextID;
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => { pending.delete(id); reject(new Error(`CDP timeout: ${method}`)); }, 5000);
    pending.set(id, { resolve, reject, timer });
    socket.send(JSON.stringify({ id, method, params }));
  });
}
async function evaluate(expression) {
  const result = await command("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
  assert.equal(result.exceptionDetails, undefined, "browser evaluation failed");
  return result.result.value;
}
async function snapshot() {
  const names = (await readdir(registration.directory)).filter(name => /^localpost_[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\.dat$/.test(name));
  assert.ok(names.length <= 3, "unexpected file count");
  const records = [];
  for (const name of names) {
    const file = path.join(registration.directory, name);
    const bytes = await readRegularFileBounded(file, 1024);
    assert.ok(bytes.length > 0, "empty delivery");
    records.push({ name, body: bytes.toString("utf8") });
  }
  return records;
}
try {
  const response = await request(`${api}/registrations`, {
    method: "POST", headers: { "Content-Type": "application/json" }, body: "{}",
  });
  assert.equal(response.status, 200, "registration failed");
  const created = await response.json();
  endpoint(service.href, created?.id);
  // A valid ID from this POST is owned even if later response validation fails.
  // Cleanup retires that ID through the API and never removes a returned path.
  registration = created;
  assert.deepEqual(Object.keys(created).sort(), ["directory", "id"]);
  assert.equal(created.id, created.id.toLowerCase());
  assert.ok(path.isAbsolute(created.directory));
  assert.equal(created.directory, path.join(root, created.id));
  assert.equal(await realpath(created.directory), created.directory);
  assert.deepEqual(await snapshot(), []);

  browser = spawn(values.chrome, ["--headless=new", "--remote-debugging-port=0",
    `--user-data-dir=${profile}`, "--no-first-run", "--no-default-browser-check", "about:blank"],
  { windowsHide: true, stdio: "ignore" });
  let launchError;
  browser.on("error", error => { launchError = error; });
  const port = await until(async () => {
    if (launchError) throw launchError;
    assert.equal(browser.exitCode, null, "Chrome exited early");
    try { return (await readFile(path.join(profile, "DevToolsActivePort"), "utf8")).split("\n")[0]; }
    catch (error) { if (error.code !== "ENOENT") throw error; return null; }
  }, "Chrome debugging endpoint");
  const targets = await (await request(`http://127.0.0.1:${port}/json/list`)).json();
  const page = targets.find(target => target.type === "page");
  assert.ok(page, "Chrome page unavailable");
  socket = new WebSocket(page.webSocketDebuggerUrl);
  socket.addEventListener("message", ({ data }) => {
    const message = JSON.parse(data);
    if (message.id) {
      const item = pending.get(message.id);
      if (!item) return;
      clearTimeout(item.timer);
      pending.delete(message.id);
      if (message.error) item.reject(new Error("CDP command failed"));
      else item.resolve(message.result);
    } else if (message.method === "Network.requestWillBeSent") {
      const { requestId, request: outgoing } = message.params;
      if (outgoing.url === endpoint(service.href, registration.id)) {
        network.push({ requestId, method: outgoing.method, body: outgoing.postData });
      }
    } else if (message.method === "Network.responseReceived") {
      const item = network.find(entry => entry.requestId === message.params.requestId);
      if (item) {
        item.status = message.params.response.status;
        item.allowOrigin = Object.entries(message.params.response.headers)
          .find(([key]) => key.toLowerCase() === "access-control-allow-origin")?.[1];
      }
    }
  });
  await Promise.race([
    once(socket, "open"),
    sleep(5000).then(() => { throw new Error("CDP connection timeout"); }),
  ]);
  await command("Network.enable");
  await command("Page.enable");
  await command("Page.navigate", { url: pilot.href });
  await until(() => evaluate("Boolean(document.getElementById('run')?.textContent)"), "pilot mount");
  const runID = await evaluate("document.getElementById('run').textContent");
  await evaluate(`document.getElementById('base').value = ${JSON.stringify(service.origin)};
    document.getElementById('registration').value = ${JSON.stringify(registration.id)};
    document.getElementById('enabled').checked = true; true`);
  for (const sequence of [1, 2]) {
    await evaluate("document.getElementById('add').click(); true");
    await until(() => evaluate("document.getElementById('delivery').textContent === 'sent'"), "delivery");
    assert.equal(await evaluate("document.getElementById('count').textContent"), String(sequence));
  }
  const files = await snapshot();
  assert.equal(files.length, 2);
  const posts = network.filter(item => item.method === "POST");
  assert.equal(posts.length, 2);
  for (const sequence of [1, 2]) {
    const body = JSON.stringify(diagnostic(runID, sequence, sequence));
    assert.equal(files.filter(file => file.body === body).length, 1, "delivery content mismatch");
    assert.equal(posts[sequence - 1].body, body);
    assert.equal(posts[sequence - 1].status, 200);
    assert.equal(posts[sequence - 1].allowOrigin, "*");
  }
  assert.ok(network.some(item => item.method === "OPTIONS" && item.status === 204 && item.allowOrigin === "*"), "CORS preflight missing");
  const deleted = await request(`${api}/registrations/${registration.id}`, { method: "DELETE" });
  assert.equal(deleted.status, 200);
  retired = true;
  await evaluate("document.getElementById('add').click(); true");
  await until(() => evaluate("document.getElementById('delivery').textContent === 'http_error'"), "retired delivery failure");
  assert.equal(await evaluate("document.getElementById('count').textContent"), "3");
  await sleep(300);
  assert.deepEqual((await snapshot()).sort((a, b) => a.name.localeCompare(b.name)), files.sort((a, b) => a.name.localeCompare(b.name)));
  assert.equal(network.filter(item => item.method === "POST").length, 3);
  assert.equal(network.filter(item => item.method === "POST")[2].status, 404);
  await writeFile(path.join(evidence, "proof.json"), JSON.stringify({
    apiVersion: "2026-11-05", browser: (await command("Browser.getVersion")).product,
    registration, runID, network, files, retired: true, countAfterFailure: 3,
  }, null, 2));
  console.log("PASS: two browser deliveries matched files; retired mailbox preserved counter and files.");
  console.log(`Evidence: ${path.join(evidence, "proof.json")}`);
} finally {
  try {
    if (registration && !retired) {
      const response = await request(`${api}/registrations/${registration.id}`, { method: "DELETE" });
      assert.equal(response.status, 200, "registration cleanup failed");
    }
  } finally {
    if (socket?.readyState === WebSocket.OPEN) {
      try { await command("Browser.close"); } catch { /* Chrome may close before replying. */ }
      socket.close();
    } else if (socket) {
      socket.close();
    }
    if (browser && browser.exitCode === null) {
      await Promise.race([once(browser, "exit"), sleep(5000)]);
      if (browser.exitCode === null) {
        browser.kill();
        await Promise.race([once(browser, "exit"), sleep(5000)]);
        assert.notEqual(browser.exitCode, null, "owned Chrome did not stop");
      }
    }
    for (const item of pending.values()) clearTimeout(item.timer);
  }
}
