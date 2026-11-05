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
const mixed = option("--model-domain", "closed") === "mixed";
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
    } else {
      assert.equal(ignored.modelEncodes, 0, "Unchanged eligible model must reuse its successful next encoding");
      if (mixed) {
        assert.equal(ignored.preparedEncodes, 20, "Both logical invocations must prepare codecs");
        assert.equal(ignored.preparedFallbacks, 0, "Mixed model must use the proven field path");
        assert.equal(ignored.fieldEncodes, 20, "Unknown page must run on every logical invocation");
      } else {
        assert.equal(ignored.fieldModelSteps, 10, "Each ignored key must use its closed field certificate");
        assert.equal(ignored.fieldEncodes, 0, "Ignored keys must not encode unrelated fields");
      }
      assert.equal(ignored.materializedBytes, 0, "Ignored keys must reuse the exact stored payload");
      assert.equal(ignored.bridgeBytes, 0, "Ignored keys must reuse the existing browser string");
      assert(ignored.encodeComparedNodes <= 2560, "Identity witness exceeded its fixed budget");

      assert.equal(ignored.storageWrites, 10, "This stage must preserve storage writes");
      for (const phase of ["updateCalls", "viewCalls", "postCalls"]) assert.equal(ignored[phase], 10, "Incremental persistence suppressed transition work");
    }
    const typing = await measure("typing", count, () => input.pressSequentially("abcdefghij"));
    assert.equal(typing.steps, 20, "Each letter must retain keydown and input messages");
    assert.equal(typing.modelEncodes, mode === "reference" ? 40 : 0, "Typing encode count changed");
    if (mode !== "reference") {
      if (mixed) {
        assert.equal(typing.preparedEncodes, 40, "Typing must retain both strict codec preparations");
        assert.equal(typing.preparedFallbacks, 0, "Mixed typing unexpectedly used Full");
        assert.equal(typing.fieldEncodes, 60, "Only unknown page and changed text/brand should convert");
      } else {
        assert.equal(typing.fieldModelSteps, 20, "Each typing message must use field certificates");
        assert.equal(typing.fieldEncodes, 10, "Only the input field should be encoded");
        assert.equal(typing.fieldEncodedBytes, 75, "Typing encoded unrelated model bytes");
      }
    }
    assert.equal(await input.inputValue(), "abcdefghij", "Typing lost input");
    assert.equal(await page.locator("#echo").textContent(), "abcdefghij", "Model and input diverged");
    if (mixed) assert.equal(await page.locator("#brand").textContent(), "abcdefghij", "Nested form and input diverged");
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
  if (mixed) assert.equal(await page.locator("#brand").textContent(), "abcdefghij", "Reload lost nested form state");
  if (mixed) {
    await page.locator("#pressure").click();
    const pressureInput = page.locator("#typing");
    const priorText = await pressureInput.inputValue();
    await pressureInput.focus();
    const pressure = await measure("raw-pressure-typing", 10000, () => pressureInput.pressSequentially("X"));
    assert.equal(pressure.steps, 2, "Pressure typing lost key/input messages");
    assert.equal(pressure.storageWrites, 0, "Aggregate oversized model exceeded the unchanged cap");
    assert.equal(pressure.modelEncodes, mode === "reference" ? 4 : 0, "Pressure typing used unexpected Full execution");
    if (mode === "incremental") {
      assert.equal(pressure.fieldEncodes, 6, "Evicted unchanged products were converted before publication");
      assert.equal(pressure.fieldEncodedBytes, 150, "Evicted unchanged products increased typing work");
      assert.equal(pressure.deferredFieldEncodes, 0, "Oversized snapshot reconstructed unneeded bytes");
      assert.equal(pressure.materializedBytes, 0, "Oversized pressure snapshot was joined");
      assert.equal(pressure.bridgeBytes, 0, "Oversized pressure snapshot crossed the browser bridge");
    }
    assert.equal(await pressureInput.inputValue(), priorText + "X", "Pressure typing lost input");
    assert.equal(await page.locator("#brand").textContent(), priorText + "X", "Pressure typing lost nested form state");
    const shrink = await measure("raw-pressure-shrink", 10000, () => page.locator("#unpressure").click());
    assert.equal(shrink.steps, 1, "Pressure removal dispatched multiple messages");
    assert.equal(shrink.storageWrites, 1, "Under-cap state was not published after shrink");
    if (mode === "incremental") {
      assert.equal(shrink.deferredFieldEncodes, 1, "Shrink must reconstruct exactly the evicted products leaf");
      assert(shrink.deferredFieldEncodedBytes > 500000 && shrink.deferredFieldEncodedBytes < 1500000, "Shrink did not reconstruct the intended bounded products leaf");
    }
    const recovered = await measure("raw-pressure-recovered-key", 10000, () => pressureInput.dispatchEvent("keydown", { key: "ArrowLeft" }));
    assert.equal(recovered.steps, 1, "Recovered ignored key did not execute once");
    assert.equal(recovered.storageWrites, 1, "Recovered ignored key did not reassert storage");
    if (mode === "incremental") {
      assert.equal(recovered.deferredFieldEncodes, 0, "Resolved products were not retained in the new cache");
      assert.equal(recovered.materializedBytes, 0, "Recovered ignored key rebuilt the unchanged snapshot");
      assert.equal(recovered.bridgeBytes, 0, "Recovered ignored key copied the existing browser string");
    }
    await page.reload({ waitUntil: "networkidle" });
    await page.waitForFunction(() => globalThis.__skyStepProfileReady === true);
    assert.equal(await page.locator("#items").textContent(), "10000", "Shrink/reload lost products");
    assert.equal(await page.locator("#typing").inputValue(), priorText + "X", "Shrink/reload lost typed state");
    assert.equal(await page.locator("#brand").textContent(), priorText + "X", "Shrink/reload lost nested state");
  }
  await page.locator("#huge").click();
  await page.waitForFunction(() => document.querySelector("#items")?.textContent === "100000");
  const hugeInput = page.locator("#typing");
  await hugeInput.fill("");
  const oversized = await measure("oversized-typing", 100000, async () => {
    // Keep the same messages, but bound each automation action separately.
    // The known slow reference path can exceed the default 30-second action
    // timeout for ten characters together. Browser/guard deadlines stay intact.
    for (const letter of "abcdefghij") await hugeInput.pressSequentially(letter);
  });
  assert.equal(oversized.steps, 20, "Oversized retained data dropped typing messages");
  assert.equal(oversized.storageWrites, 0, "Oversized snapshot exceeded the unchanged persistence cap");
  assert.equal(oversized.modelEncodes, mode === "reference" ? 40 : 0, "Oversized typing unexpectedly encoded the full model");
  if (mode !== "reference") {
    assert.equal(oversized.fieldEncodes, mixed ? 60 : 10, "Oversized typing encoded unrelated fields");
    if (mixed) {
      assert.equal(oversized.preparedEncodes, 40, "Oversized typing lost strict preparations");
      assert.equal(oversized.preparedFallbacks, 0, "Oversized typing used Full");
    } else assert.equal(oversized.fieldEncodedBytes, 75, "Oversized typing encoded unrelated bytes");
    assert.equal(oversized.materializedBytes, 0, "Oversized snapshot was materialized");
    assert.equal(oversized.bridgeBytes, 0, "Oversized snapshot crossed the wasm bridge");
  }
  await page.locator("#rows").click();
  await page.getByText("visible row 200", { exact: true }).waitFor();
  await hugeInput.focus();
  await hugeInput.evaluate(el => el.setSelectionRange(el.value.length, el.value.length));
  const visible = await measure("visible-rows-typing", 100000, () => hugeInput.pressSequentially("k"));
  assert.equal(visible.steps, 2, "Large view dropped typing messages");
  assert.equal(visible.viewCalls, 2, "This persistence stage must preserve full view execution");
  assert.equal(await hugeInput.inputValue(), "abcdefghijk", "Large view lost typed input");
  await hugeInput.evaluate(el => el.setSelectionRange(3, 3));
  await hugeInput.pressSequentially("Z");
  assert.equal(await hugeInput.inputValue(), "abcZdefghijk", "Large view lost a mid-string edit");
  assert.equal(await page.locator("#echo").textContent(), "abcZdefghijk", "Mid-string edit diverged from the model");
  if (mixed) assert.equal(await page.locator("#brand").textContent(), "abcZdefghijk", "Mid-string edit lost nested form state");
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
  if (mixed && mode === "incremental") {
    const typingRuns = measurements.filter(m => m.scenario === "typing" || m.scenario === "oversized-typing");
    assert.equal(typingRuns.length, 3, "Missing scaling measurements");
    assert(typingRuns.every(m => m.fieldEncodedBytes === typingRuns[0].fieldEncodedBytes), "Encoding work grew with unchanged retained data");
  }
  const report = { modelDomain: mixed ? "mixed" : "closed", browser: engineName, cpuRate: throttle, mode, passed: true, measurements };
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
