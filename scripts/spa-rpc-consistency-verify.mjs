#!/usr/bin/env node
// scripts/spa-rpc-consistency-verify.mjs
//
// Browser e2e for Sky.Spa (web:app) RPC consistency. Every check is the answer
// Sky.Live gives for the same Msg sequence (Live runs the whole TEA loop on the
// server, one Msg at a time, in dispatch order). Drives the fixture
// rust/crates/sky/tests/fixtures/spa-rpc-consistency in headless Chromium:
//
//   race      Inc twice (slow RPC) -> count=2; a draft typed while Save is in
//             flight survives the response (SPA-1)
//   order     the 1st of two RPCs answers last -> the 2nd still wins (SPA-2)
//   guard     a guarded client Msg is rejected; the server guard sees the
//             field it reads (SPA-4)
//   follow-up a server branch's `Cmd.perform … (\_ -> Load)` runs Load (SPA-3)
//   dedupe    a request the server ran but whose response was lost is retried
//             with the same id and does NOT run twice (SPA-6)
//   retry     every failed/queued RPC runs, in order, on Retry (SPA-7)
//   reload    client scratch state is restored (SPA-8); a field only server
//             branches write comes from the SSR seed, not localStorage (K5)
//
// Usage: node scripts/spa-rpc-consistency-verify.mjs <backend-app> [--port N]
// Exit: 0 PASS · 2 FAIL · 1 harness error.
import pw from "playwright";
const { chromium } = pw;
import { spawn } from "node:child_process";
import { dirname, join } from "node:path";
import { writeFileSync } from "node:fs";

function arg(name, def) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : def;
}
const BACKEND = process.argv[2];
if (!BACKEND) {
  console.error("usage: spa-rpc-consistency-verify.mjs <backend-app> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9221"));
const BACKEND_DIR = dirname(dirname(BACKEND));
writeFileSync(join(BACKEND_DIR, "seed.txt"), "start");
writeFileSync(join(BACKEND_DIR, "server.txt"), "v1");

const proc = spawn(BACKEND, [], { cwd: BACKEND_DIR, env: { ...process.env, PORT: String(PORT) } });
let serverLog = "";
proc.stdout.on("data", (d) => (serverLog += d));
proc.stderr.on("data", (d) => (serverLog += d));
const listening = new Promise((res) => proc.stdout.on("data", (d) => d.toString().includes("Sky server listening") && res()));

const failures = [];
function check(step, got, want) {
  const ok = got === want;
  console.log(`${ok ? "ok  " : "FAIL"} ${step}: ${JSON.stringify(got)} (want ${JSON.stringify(want)})`);
  if (!ok) failures.push(step);
}
const text = (page, id) => page.locator("#" + id).innerText();

// RPC routing knobs, flipped per scenario.
let delayFor = () => 0; // ms before a request is forwarded
let mode = "pass"; // pass | dropResponse | offline
let rpcSeen = [];

try {
  await Promise.race([
    listening,
    new Promise((_, rej) => setTimeout(() => rej(new Error("backend never listened\n" + serverLog)), 20000)),
  ]);
  const browser = await chromium.launch({ headless: true });
  const page = await browser.newPage();
  const pageErrors = [];
  page.on("pageerror", (e) => pageErrors.push(`[pageerror] ${e.message}`));
  await page.route("**/_rpc/**", async (route) => {
    const url = route.request().url();
    rpcSeen.push(url);
    const d = delayFor(url);
    if (d > 0) await new Promise((r) => setTimeout(r, d));
    if (mode === "offline") return route.abort("internetdisconnected");
    if (mode === "dropResponse") {
      // The server RUNS the request, but the client never sees the answer.
      await route.fetch();
      mode = "pass";
      return route.abort("connectionreset");
    }
    return route.continue();
  });

  await page.goto(`http://127.0.0.1:${PORT}/`, { waitUntil: "networkidle" });
  await page.waitForTimeout(1500); // wasm boot + hydrate

  // ---- race (SPA-1) --------------------------------------------------------
  delayFor = () => 500;
  await page.click("#inc");
  await page.click("#inc");
  await page.waitForTimeout(2200);
  check("race: Inc x2 (slow RPC)", await text(page, "count"), "count=2");

  await page.fill("#draft", "first");
  await page.waitForTimeout(300);
  await page.click("#save");
  await page.waitForTimeout(80);
  // A client edit made WHILE Save is on the wire. Live applies Save (draft := "")
  // and then this edit, so the edit must survive the Save response.
  await page.fill("#draft", "second");
  await page.waitForTimeout(1500);
  check("race: saved after Save+type", await text(page, "saved"), "saved=first:start");
  check("race: draft typed during Save survives", await text(page, "draftv"), "draft=second");
  check("race: input keeps the typed text", await page.inputValue("#draft"), "second");

  // ---- order (SPA-2) -------------------------------------------------------
  let n = 0;
  delayFor = (u) => (u.includes("SetName") ? (n++ === 0 ? 900 : 50) : 0);
  await page.locator("#nameIn").pressSequentially("ab", { delay: 100 });
  await page.waitForTimeout(2200);
  check("order: last-dispatched name wins", await text(page, "name"), "name=ab");
  delayFor = () => 0;

  // ---- guard (SPA-4) -------------------------------------------------------
  await page.click("#unlock");
  await page.waitForTimeout(300);
  check("guard: rejected client Msg does not run", await text(page, "unlocked"), "unlocked=no");
  await page.click("#secure");
  await page.waitForTimeout(600);
  check("guard: server branch still locked", await text(page, "wrote"), "wrote=no");
  await page.click("#enable");
  await page.waitForTimeout(300);
  await page.click("#secure");
  await page.waitForTimeout(800);
  check("guard: server guard sees the field it reads", await text(page, "wrote"), "wrote=yes");

  // ---- follow-up Cmd (SPA-3) -----------------------------------------------
  await page.click("#saveThenLoad");
  await page.waitForTimeout(1200);
  check("follow-up: server branch's Cmd ran Load", await text(page, "loaded"), "loaded=hello");
  await page.click("#serverThenPure");
  await page.waitForTimeout(800);
  check("follow-up: pure perform into a client arm", await text(page, "bumped"), "bumped=7");

  // ---- dedupe (SPA-6) ------------------------------------------------------
  mode = "dropResponse";
  await page.click("#hit");
  await page.waitForTimeout(800);
  const overlay = page.locator("#sky-spa-neterror button");
  check("dedupe: retry overlay shown", await overlay.isVisible(), true);
  await overlay.click();
  await page.waitForTimeout(1000);
  check("dedupe: retried Hit ran its effect once", await text(page, "hits"), "hits=1");

  // ---- retry keeps every failed/queued RPC in order (SPA-7) ----------------
  const num = async (id) => Number((await text(page, id)).split("=")[1]);
  const hits0 = await num("hits");
  const count0 = await num("count");
  mode = "offline";
  await page.click("#hit");
  await page.waitForTimeout(400);
  await page.click("#inc");
  await page.waitForTimeout(600);
  mode = "pass";
  await overlay.click();
  await page.waitForTimeout(1500);
  check("retry: failed Hit re-ran", await num("hits"), hits0 + 1);
  check("retry: Inc queued/failed behind it also ran", await num("count"), count0 + 1);

  // ---- reload: persistence (SPA-8) + SSR seed for server-only fields (K5) --
  await page.click("#touch");
  await page.waitForTimeout(600);
  check("reload: server field before reload", await text(page, "server"), "server=v1");
  await page.fill("#noteIn", "kept-note");
  await page.waitForTimeout(400);
  // The server truth changes behind the client's back (another tab, a job).
  writeFileSync(join(BACKEND_DIR, "server.txt"), "v2");
  await page.reload({ waitUntil: "networkidle" });
  await page.waitForTimeout(1500);
  check("reload: client scratch state restored", await text(page, "note"), "note=kept-note");
  check("reload: guard-permitted client flag restored", await text(page, "unlocked"), "unlocked=yes");
  check("reload: server-only field from the SSR seed", await text(page, "server"), "server=v2");

  await browser.close();
  for (const m of pageErrors) console.log(m);
  if (pageErrors.length) failures.push("page errors");
  console.log(failures.length ? `VERDICT=FAIL ${failures.join("; ")}` : "VERDICT=PASS web:app matches Sky.Live on every scenario");
  process.exitCode = failures.length ? 2 : 0;
} catch (e) {
  console.error("HARNESS ERROR:", e.message);
  console.error(serverLog.slice(-2000));
  process.exitCode = 1;
} finally {
  proc.kill("SIGKILL");
}
