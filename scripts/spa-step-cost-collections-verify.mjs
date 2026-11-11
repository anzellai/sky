#!/usr/bin/env node
// Public collections step-cost/conformance fixture. Emits measurements and synthetic canonical-byte hashes.
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
const mixed = true;
const collection = option("--collection", "dict");
assert(["dict", "tuple"].includes(collection), "Unknown collection mode");
const mode = option("--mode", "reference");
const debugProgress = option("--progress", "0") === "1";
const progressStarted = performance.now();
const progressState = { stage: "startup", items: 0, expectedSteps: 0, actualSteps: 0 };
let progressPage;
let progressQueryPending = false;
let progressReports = 0;
// Diagnostics only: bounded numeric output, never read application data/logs.
const progressTimer = debugProgress ? setInterval(() => {
  if (progressReports++ >= 30) return;
  process.stderr.write(JSON.stringify({ progress: "heartbeat", ...progressState, queryPending: progressQueryPending, elapsedMs: Math.round(performance.now() - progressStarted) }) + "\n");
  if (!progressPage || progressQueryPending) return;
  progressQueryPending = true;
  void progressPage.evaluate(() => globalThis.__skyStepProfile?.steps || 0).then(steps => {
    if (Number.isSafeInteger(steps) && steps >= 0) progressState.actualSteps = steps;
  }).catch(() => {}).finally(() => { progressQueryPending = false; });
}, 10000) : null;
const mark = (stage, items = 0) => { progressState.stage = stage; progressState.items = items; if (debugProgress) process.stderr.write(JSON.stringify({ progress: stage, items, elapsedMs: Math.round(performance.now() - progressStarted) }) + "\n"); };
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
const checkpoints = [];
const oversizedCheckpoints = [];
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
    globalThis.__fixtureStorageRemoves = 0;
    const original = Storage.prototype.setItem;
    Storage.prototype.setItem = function(key, value) {
      if (key.startsWith("sky:spa:model")) globalThis.__fixtureStorageWrites++;
      return original.call(this, key, value);
    };
    const remove = Storage.prototype.removeItem;
    Storage.prototype.removeItem = function(key) {
      if (key.startsWith("sky:spa:model")) globalThis.__fixtureStorageRemoves++;
      return remove.call(this, key);
    };
  }, mode === "reference");
  const page = await context.newPage();
  progressPage = page;
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
    ...globalThis.__skyStepProfile, steps: globalThis.__skyStepProfile?.steps || 0,
    storageWrites: globalThis.__fixtureStorageWrites,
    storageRemoves: globalThis.__fixtureStorageRemoves,
  }));
  // DOM paint precedes persistence. Wait for the whole TEA step to publish
  // its counters before issuing another automation action. The outer 300s
  // watchdog bounds this promise; no Playwright timeout is increased.
  const completedSteps = minimum => {
    assert(Number.isSafeInteger(minimum) && minimum >= 0, "Invalid completed-step counter");
    progressState.expectedSteps = minimum;
    return page.evaluate(minimum => new Promise(resolve => {
      const poll = () => {
        const actual = globalThis.__skyStepProfile?.steps || 0;
        if (actual >= minimum) resolve(actual);
        else setTimeout(poll, 10);
      };
      poll();
    }), minimum).then(actual => {
      progressState.actualSteps = actual;
      return actual;
    });
  };
  const expectedSteps = {
    "ignored-key": 10, typing: 20, "async-command": 2,
    "collection-noop": 1, "collection-change": 1,
    "raw-pressure-typing": 2, "raw-pressure-shrink": 1,
    "raw-pressure-recovered-key": 1, "oversized-typing": 20,
    "visible-rows-typing": 2,
    "oversized-dict-load": 1, "oversized-dict-change": 1,
  };
  async function measure(scenario, items, action) {
    mark("start-" + scenario, items);
    const before = await snapshot();
    const cpuBefore = cdp ? (await cdp.send("Performance.getMetrics")).metrics.find(m => m.name === "TaskDuration").value : 0;
    assert(Number.isInteger(expectedSteps[scenario]), "Missing completed-step expectation");
    await action();
    await completedSteps(before.steps + expectedSteps[scenario]);
    const after = await snapshot();
    const cpuAfter = cdp ? (await cdp.send("Performance.getMetrics")).metrics.find(m => m.name === "TaskDuration").value : 0;
    const result = { scenario, items };
    for (const key of Object.keys(after)) result[key] = after[key] - (before[key] || 0);
    if (cdp) result.browserTaskNanos = Math.round((cpuAfter - cpuBefore) * 1e9);
    measurements.push(result);
    mark("end-" + scenario, items);
    return result;
  }
  const beforeChoice = (await snapshot()).steps;
  await page.locator("#" + collection).click();
  await completedSteps(beforeChoice + 1);
  assert.equal(await page.locator("#collection").textContent(), collection);
  function expectedCollectionBytes(count, changed) {
    let bytes = 2;
    for (let i = 1; i <= count; i++) {
      if (i > 1) bytes++;
      const value = changed && i === 1 ? "changed" : "x";
      bytes += collection === "dict"
        ? JSON.stringify("key-" + i).length + 1 + JSON.stringify(value).length
        : JSON.stringify({ v0: i, v1: value }).length;
    }
    return bytes;
  }
  async function assertStoredCollection(name, count, changed) {
    const proof = await page.evaluate(async ({ collection, count, changed }) => {
      const raw = localStorage.getItem("sky:spa:model:v2");
      if (!raw) return { valid: false };
      const model = JSON.parse(raw);
      const editor = model.editor;
      const value = "x";
      let valid = model.collection === collection && editor && editor.products.length === 0;
      if (valid && collection === "dict") {
        valid = editor.pairs.length === 0 && Object.keys(editor.lookup).length === count;
        for (let i = 1; valid && i <= count; i++) valid = editor.lookup["key-" + i] === (changed && i === 1 ? "changed" : value);
      } else if (valid) {
        valid = Object.keys(editor.lookup).length === 0 && editor.pairs.length === count;
        for (let i = 0; valid && i < count; i++) valid = editor.pairs[i].v0 === i + 1 && editor.pairs[i].v1 === (changed && i === 0 ? "changed" : value);
      }
      const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(raw));
      return { valid: Boolean(valid), digest: Array.from(new Uint8Array(digest), n => n.toString(16).padStart(2, "0")).join("") };
    }, { collection, count, changed });
    assert(proof.valid, "Stored model lost canonical collection shape/contents or retained both modes");
    checkpoints.push({ name, digest: proof.digest });
  }
  for (const [id, count] of [["small", 1000], ["large", 10000]]) {
    mark("load-start", count);
    const beforeLoad = (await snapshot()).steps;
    await page.locator("#" + id).click();
    await completedSteps(beforeLoad + 1);
    await page.waitForFunction(n => document.querySelector("#items")?.textContent === String(n), count);
    assert.equal(await page.locator("#sample").textContent(), "x");
    await assertStoredCollection(`loaded-${count}`, count, false);
    const input = page.locator("#typing");
    if (await input.inputValue() !== "") {
      const beforeClear = (await snapshot()).steps;
      await input.fill("");
      await completedSteps(beforeClear + 1);
    }
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
        assert.equal(typing.fieldEncodedBytes, 1390, "Typing encoded unchanged collections");
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
    const noop = await measure("collection-noop", count, () => page.locator("#noop-collection").click());
    assert.equal(noop.steps, 1, "Collection no-op dispatched multiple messages");
    if (mode === "incremental") {
      assert.equal(noop.fieldEncodes, 2, "Absent removal encoded unchanged collection contents");
      assert.equal(noop.materializedBytes, 0, "Collection no-op rebuilt the stored payload");
    }
    await assertStoredCollection(`noop-${count}`, count, false);
    const changed = await measure("collection-change", count, () => page.locator("#change-collection").click());
    assert.equal(changed.steps, 1, "Entry change dispatched multiple messages");
    assert.equal(await page.locator("#sample").textContent(), "changed", "Entry mutation lost its value");
    if (mode === "incremental") {
      assert.equal(changed.fieldEncodes, 3, "Changed root must receive one canonical atomic conversion plus both page invocations");
      assert.equal(changed.fieldEncodedBytes, expectedCollectionBytes(count, true) + 2 * JSON.stringify({ tag: "Home", v0: "synthetic" }).length, "Changed root did not receive exact full canonical encoding");
    }
    await assertStoredCollection(`changed-${count}`, count, true);
  }
  mark("reload-start");
  await page.reload({ waitUntil: "networkidle" });
  await page.waitForFunction(() => globalThis.__skyStepProfileReady === true);
  assert.equal(await page.locator("#typing").inputValue(), "abcdefghij", "Reload lost typed state");
  assert.equal(await page.locator("#items").textContent(), "10000", "Reload lost unrelated data");
  if (mixed) assert.equal(await page.locator("#brand").textContent(), "abcdefghij", "Reload lost nested form state");
  assert.equal(await page.locator("#sample").textContent(), "changed", "Reload lost the collection update");
  await assertStoredCollection("reloaded-10000", 10000, true);
  if (mixed) {
    mark("pressure-load-start", 10000);
    const beforePressure = (await snapshot()).steps;
    await page.locator("#pressure").click();
    await completedSteps(beforePressure + 1);
    const pressureInput = page.locator("#typing");
    const priorText = await pressureInput.inputValue();
    await pressureInput.focus();
    const pressure = await measure("raw-pressure-typing", 10000, () => pressureInput.pressSequentially("X"));
    assert.equal(pressure.steps, 2, "Pressure typing lost key/input messages");
    assert.equal(pressure.storageWrites, 0, "Aggregate oversized model exceeded the unchanged cap");
    assert.equal(pressure.modelEncodes, mode === "reference" ? 4 : 0, "Pressure typing used unexpected Full execution");
    if (mode === "incremental") {
      assert.equal(pressure.fieldEncodes, 6, "Evicted unchanged collection were converted before publication");
      assert.equal(pressure.fieldEncodedBytes, 150, "Evicted unchanged collection increased typing work");
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
      assert.equal(shrink.deferredFieldEncodes, 1, "Shrink must reconstruct exactly the evicted collection leaf");
      assert.equal(shrink.deferredFieldEncodedBytes, expectedCollectionBytes(10000, true), "Shrink did not reconstruct the exact intended collection leaf");
    }
    await assertStoredCollection("pressure-shrink", 10000, true);
    const recovered = await measure("raw-pressure-recovered-key", 10000, () => pressureInput.dispatchEvent("keydown", { key: "ArrowLeft" }));
    assert.equal(recovered.steps, 1, "Recovered ignored key did not execute once");
    assert.equal(recovered.storageWrites, 1, "Recovered ignored key did not reassert storage");
    if (mode === "incremental") {
      assert.equal(recovered.deferredFieldEncodes, 0, "Resolved collection were not retained in the new cache");
      assert.equal(recovered.materializedBytes, 0, "Recovered ignored key rebuilt the unchanged snapshot");
      assert.equal(recovered.bridgeBytes, 0, "Recovered ignored key copied the existing browser string");
    }
    mark("reload-start");
  await page.reload({ waitUntil: "networkidle" });
    await page.waitForFunction(() => globalThis.__skyStepProfileReady === true);
    assert.equal(await page.locator("#items").textContent(), "10000", "Shrink/reload lost collection");
    assert.equal(await page.locator("#typing").inputValue(), priorText + "X", "Shrink/reload lost typed state");
    assert.equal(await page.locator("#brand").textContent(), priorText + "X", "Shrink/reload lost nested state");
    assert.equal(await page.locator("#sample").textContent(), "changed", "Shrink/reload lost the collection update");
  }
  if (collection === "dict") {
    const storedProof = () => page.evaluate(async () => {
      const raw = localStorage.getItem("sky:spa:model:v2");
      if (!raw) return { present: false };
      const bytes = new TextEncoder().encode(raw);
      const digest = await crypto.subtle.digest("SHA-256", bytes);
      return { present: true, bytes: bytes.length, digest: Array.from(new Uint8Array(digest), n => n.toString(16).padStart(2, "0")).join("") };
    });
    const stored = await storedProof();
    assert(stored.present && stored.bytes < 3000000, "Missing under-cap baseline snapshot");
    const loaded = await measure("oversized-dict-load", 64, () => page.locator("#oversized-dict").click());
    assert.equal(await page.locator("#items").textContent(), "64");
    assert.equal(await page.locator("#sample").textContent(), "x".repeat(128));
    assert.equal(loaded.storageWrites, 0, "Oversized Dict load published a snapshot");
    assert.equal(loaded.storageRemoves, 0, "Oversized Dict load removed the previous snapshot");
    assert.deepEqual(await storedProof(), stored, "Oversized Dict load changed persisted state");
    oversizedCheckpoints.push({ name: "loaded", ...stored });

    // The completed load warms the certificate; only this one-entry change is
    // measured below. Sixty-three 64KiB values remain above the unchanged cap.
    const changed = await measure("oversized-dict-change", 64, () => page.locator("#change-collection").click());
    assert.equal(await page.locator("#sample").textContent(), "changed");
    assert.equal(await page.locator("#items").textContent(), "64");
    for (const phase of ["updateCalls", "viewCalls", "postCalls"]) assert.equal(changed[phase], 1);
    assert.equal(changed.storageWrites, 0, "Changed oversized Dict published a snapshot");
    assert.equal(changed.storageRemoves, 0, "Changed oversized Dict removed persisted state");
    assert.equal(changed.bridgeBytes, 0, "Changed oversized Dict crossed the storage bridge");
    if (mode === "incremental") {
      assert.equal(changed.modelEncodes, 0, "Changed primitive Dict fell back to the full model encoder");
      assert.equal(changed.preparedEncodes, 2, "Changed primitive Dict skipped strict preparations");
      assert.equal(changed.preparedFallbacks, 0);
      assert.equal(changed.materializedBytes, 0);
      assert(changed.encodedBytes + changed.fieldEncodedBytes + changed.deferredFieldEncodedBytes < 1024, "Changed primitive Dict encoded unchanged large values");
    } else {
      assert.equal(changed.modelEncodes, 2);
      assert(changed.encodedBytes > 3000000, "Reference did not encode the oversized model");
    }
    assert.deepEqual(await storedProof(), stored, "Changed oversized Dict changed persisted state");
    oversizedCheckpoints.push({ name: "changed", ...stored });
    await page.reload({ waitUntil: "networkidle" });
    await page.waitForFunction(() => globalThis.__skyStepProfileReady === true);
    assert.equal(await page.locator("#items").textContent(), "10000", "NoWrite reload did not restore the previous snapshot");
    assert.equal(await page.locator("#sample").textContent(), "changed");
    assert.deepEqual(await storedProof(), stored, "NoWrite reload changed persisted state");
    oversizedCheckpoints.push({ name: "reloaded", ...stored });
  }
  mark("huge-load-start", 200000);
  const beforeHuge = (await snapshot()).steps;
  await page.locator("#huge").click();
  await completedSteps(beforeHuge + 1);
  await page.waitForFunction(() => document.querySelector("#items")?.textContent === "200000");
  mark("huge-load-end", 200000);
  const hugeInput = page.locator("#typing");
  if (await hugeInput.inputValue() !== "") {
    const beforeHugeClear = (await snapshot()).steps;
    await hugeInput.fill("");
    await completedSteps(beforeHugeClear + 1);
  }
  const oversized = await measure("oversized-typing", 200000, async () => {
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
    assert.equal(oversized.fieldEncodedBytes, 1390, "Oversized typing encoded unchanged collection bytes");
    if (mixed) {
      assert.equal(oversized.preparedEncodes, 40, "Oversized typing lost strict preparations");
      assert.equal(oversized.preparedFallbacks, 0, "Oversized typing used Full");
    } else assert.equal(oversized.fieldEncodedBytes, 75, "Oversized typing encoded unrelated bytes");
    assert.equal(oversized.materializedBytes, 0, "Oversized snapshot was materialized");
    assert.equal(oversized.bridgeBytes, 0, "Oversized snapshot crossed the wasm bridge");
  }
  const beforeRows = (await snapshot()).steps;
  await page.locator("#rows").click();
  await completedSteps(beforeRows + 1);
  await page.getByText("visible row 200", { exact: true }).waitFor();
  await hugeInput.focus();
  await hugeInput.evaluate(el => el.setSelectionRange(el.value.length, el.value.length));
  const visible = await measure("visible-rows-typing", 200000, () => hugeInput.pressSequentially("k"));
  assert.equal(visible.steps, 2, "Large view dropped typing messages");
  assert.equal(visible.viewCalls, 2, "This persistence stage must preserve full view execution");
  assert.equal(await hugeInput.inputValue(), "abcdefghijk", "Large view lost typed input");
  await hugeInput.evaluate(el => el.setSelectionRange(3, 3));
  const beforeMidString = (await snapshot()).steps;
  await hugeInput.pressSequentially("Z");
  await completedSteps(beforeMidString + 2);
  assert.equal(await hugeInput.inputValue(), "abcZdefghijk", "Large view lost a mid-string edit");
  assert.equal(await page.locator("#echo").textContent(), "abcZdefghijk", "Mid-string edit diverged from the model");
  if (mixed) assert.equal(await page.locator("#brand").textContent(), "abcZdefghijk", "Mid-string edit lost nested form state");
  const before_advance = (await snapshot()).steps;
  await page.locator("#advance").click();
  await completedSteps(before_advance + 1);
  const before_select = (await snapshot()).steps;
  await page.locator("#select").click();
  await completedSteps(before_select + 1);
  assert.equal(await page.locator("#selected").textContent(), "2", "Visually equal button kept stale payload");
  const before_signin = (await snapshot()).steps;
  await page.locator("#signin").click();
  await completedSteps(before_signin + 1);
  await page.getByText("signed-in", { exact: true }).waitFor();
  const before_signout = (await snapshot()).steps;
  await page.locator("#signout").click();
  await completedSteps(before_signout + 1);
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
  const report = { collection, checkpoints, oversizedCheckpoints, modelDomain: mixed ? "mixed" : "closed", browser: engineName, cpuRate: throttle, mode, passed: true, measurements };
  if (output) await fs.writeFile(output, JSON.stringify(report, null, 2) + "\n");
  console.log(JSON.stringify(report));
} finally {
  if (progressTimer) clearInterval(progressTimer);
  mark("cleanup-browser-close-start");
  if (browser) await browser.close();
  mark("cleanup-browser-close-end");
  mark("cleanup-backend-stop-start");
  child.kill("SIGTERM");
  if (child.exitCode === null && child.signalCode === null) {
    const stopDelay = new AbortController();
    try {
      await Promise.race([
        new Promise(resolve => child.once("exit", resolve)),
        delay(5000, undefined, { signal: stopDelay.signal }),
      ]);
    } finally {
      // An early exit must not leave the losing five-second timer alive.
      // A backend that does not exit still receives the full 5000ms grace.
      stopDelay.abort();
    }
  }
  if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
  mark("cleanup-backend-stop-end");
  mark("cleanup-scratch-remove-start");
  await fs.rm(scratch, { recursive: true, force: true });
  mark("cleanup-scratch-remove-end");
}
