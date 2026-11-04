#!/usr/bin/env node
// Public step-cost/conformance fixture. Only numeric measurements are emitted.
// The backend receives a clean synthetic configuration. Never inherit app env.
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import net from "node:net";
import { spawn } from "node:child_process";
import { setTimeout as delay } from "node:timers/promises";
import pw from "playwright";
import { guardChild } from "./lib/child-guard.mjs";

const backend = process.argv[2];
assert(backend, "A freshly built fixture backend is required");
const option = (name, fallback) => {
  const i = process.argv.indexOf(name);
  return i < 0 ? fallback : process.argv[i + 1];
};
const engineName = option("--browser", "chromium");
assert(["chromium", "webkit", "firefox"].includes(engineName), "Unknown browser");
const throttle = Number(option("--cpu", "1"));
assert(Number.isFinite(throttle) && throttle >= 1, "Invalid CPU rate");
assert(throttle === 1 || engineName === "chromium", "CPU throttle requires Chromium");
const output = option("--output", null);
const mode = option("--mode", "reference");
assert(["reference", "incremental"].includes(mode), "Unknown execution mode");
const scratch = await fs.mkdtemp(path.join(os.tmpdir(), "sky-step-cost-"));
const home = path.join(scratch, "home");
await fs.mkdir(home);
const listener = net.createServer();
await new Promise(resolve => listener.listen(0, "127.0.0.1", resolve));
const port = listener.address().port;
await new Promise(resolve => listener.close(resolve));
const origin = `http://127.0.0.1:${port}`;
const child = guardChild(spawn(path.resolve(backend), [], {
  cwd: path.dirname(path.dirname(path.resolve(backend))),
  env: {
    HOME: home, PATH: "/usr/bin:/bin", LANG: "en_US.UTF-8",
    ENV: "dev", PORT: String(port), HOST: "127.0.0.1",
    SKY_DATA_DIR: path.join(scratch, "data"), SKY_LIVE_STORE: "memory",
    SKY_CSP: "strict",
  },
  stdio: ["ignore", "pipe", "pipe"],
}));
// Drain without printing: application logs may contain runtime configuration.
child.stdout.resume();
child.stderr.resume();
let browser;
const measurements = [];
try {
  let ready = false;
  for (let n = 0; n < 300; n++) {
    assert(child.exitCode === null, "Fixture backend exited before readiness");
    try { ready = (await fetch(origin + "/_sky/readyz", { signal: AbortSignal.timeout(1000) })).ok; } catch {}
    if (ready) break;
    await delay(100);
  }
  assert(ready, "Fixture backend did not become ready");
  browser = await pw[engineName].launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
  await context.addInitScript(reference => {
    globalThis.__skyStepProfileEnabled = true;
    globalThis.__skyStepReference = reference;
    globalThis.__fixtureStorageWrites = 0;
    const original = Storage.prototype.setItem;
    Storage.prototype.setItem = function(key, value) {
      if (key.startsWith("sky:spa:model")) globalThis.__fixtureStorageWrites++;
      return original.call(this, key, value);
    };
  }, mode === "reference");
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", error => errors.push(error.name));
  page.on("console", message => {
    if (message.type() === "error") errors.push("console-error");
  });
  let cdp;
  if (engineName === "chromium") {
    cdp = await context.newCDPSession(page);
    await cdp.send("Performance.enable");
    if (throttle !== 1) await cdp.send("Emulation.setCPUThrottlingRate", { rate: throttle });
  }
  await page.goto(origin, { waitUntil: "networkidle" });
  await page.waitForFunction(() => globalThis.__skyStepProfileReady === true);
  // An interaction proves actual wasm boot. SSR-only text cannot satisfy this.
  await page.locator("#advance").click();
  await page.waitForFunction(() => globalThis.__skyStepProfile?.steps > 0);
  const snapshot = () => page.evaluate(() => ({
    ...globalThis.__skyStepProfile, storageWrites: globalThis.__fixtureStorageWrites,
  }));
  async function measure(scenario, items, action) {
    const before = await snapshot();
    const cpuBefore = cdp ? (await cdp.send("Performance.getMetrics")).metrics.find(m => m.name === "TaskDuration").value : 0;
    await action();
    const after = await snapshot();
    const cpuAfter = cdp ? (await cdp.send("Performance.getMetrics")).metrics.find(m => m.name === "TaskDuration").value : 0;
    const result = { scenario, items };
    for (const key of Object.keys(after)) result[key] = after[key] - (before[key] || 0);
    if (cdp) result.browserTaskNanos = Math.round((cpuAfter - cpuBefore) * 1e9);
    measurements.push(result);
    return result;
  }
  for (const [id, count] of [["small", 1000], ["large", 10000]]) {
    await page.locator("#" + id).click();
    await page.waitForFunction(n => document.querySelector("#items")?.textContent === String(n), count);
    const input = page.locator("#typing");
    await input.fill("");
    await input.focus();
    const ignored = await measure("ignored-key", count, async () => {
      for (let i = 0; i < 10; i++) await input.dispatchEvent("keydown", { key: "ArrowLeft" });
    });
    assert.equal(ignored.steps, 10, "Ignored keys must each execute once");
    if (mode === "reference") {
      assert.equal(ignored.modelEncodes, 20, "Reference must retain both model encodes");
      assert.equal(ignored.storageWrites, 10, "Reference must retain each storage write");
      for (const phase of ["updateCalls", "viewCalls", "postCalls"]) assert.equal(ignored[phase], 10, "Reference phase count changed");
    }
    const typing = await measure("typing", count, () => input.pressSequentially("abcdefghij"));
    assert.equal(typing.steps, 20, "Each letter must retain keydown and input messages");
    if (mode === "reference") assert.equal(typing.modelEncodes, 40, "Reference typing encode count changed");
    assert.equal(await input.inputValue(), "abcdefghij", "Typing lost input");
    assert.equal(await page.locator("#echo").textContent(), "abcdefghij", "Model and input diverged");
    await measure("async-command", count, async () => {
      const previous = Number(await page.locator("#completed").innerText());
      await page.locator("#dispatch").click();
      await page.waitForFunction(n => Number(document.querySelector("#completed")?.textContent) === n + 1, previous);
      await delay(500);
      assert.equal(Number(await page.locator("#completed").innerText()), previous + 1, "Async command completed more than once");
    });
  }
  await page.reload({ waitUntil: "networkidle" });
  await page.waitForFunction(() => globalThis.__skyStepProfileReady === true);
  assert.equal(await page.locator("#typing").inputValue(), "abcdefghij", "Reload lost typed state");
  assert.equal(await page.locator("#items").textContent(), "10000", "Reload lost unrelated data");
  await page.locator("#advance").click();
  await page.locator("#select").click();
  assert.equal(await page.locator("#selected").textContent(), "2", "Visually equal button kept stale payload");
  await page.locator("#signin").click();
  await page.getByText("signed-in", { exact: true }).waitFor();
  await page.locator("#signout").click();
  await page.getByText("anonymous", { exact: true }).waitFor();
  for (const width of [390, 820, 1180, 1440]) {
    await page.setViewportSize({ width, height: 960 });
    assert(!(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1)), "Fixture horizontal overflow");
  }
  assert.equal(errors.length, 0, "Fixture emitted browser errors");
  const report = { browser: engineName, cpuRate: throttle, mode, passed: true, measurements };
  if (output) await fs.writeFile(output, JSON.stringify(report, null, 2) + "\n");
  console.log(JSON.stringify(report));
} finally {
  if (browser) await browser.close();
  child.kill("SIGTERM");
  if (child.exitCode === null && child.signalCode === null) {
    await Promise.race([new Promise(resolve => child.once("exit", resolve)), delay(5000)]);
  }
  if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
  await fs.rm(scratch, { recursive: true, force: true });
}
