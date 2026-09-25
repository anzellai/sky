#!/usr/bin/env node
// scripts/spa-identity-verify.mjs
//
// Browser e2e for the identity rule of Sky.Spa client persistence
// (runtime-go/rt/spa_persist.go spaRestoreStored). Drives
// rust/crates/sky/tests/fixtures/spa-identity-slot in headless Chromium.
// Run by scripts/spa-restore-e2e.sh.
//
// Found in a real app: one browser, two tabs on one origin, a practitioner
// signed in in tab A and a patient link (another session identity) in tab B.
// localStorage is shared by every tab of the origin and held ONE model, so a
// full load of tab A restored the patient's page and data under the
// practitioner's session.
//
// Two pages in ONE browser context share localStorage, as two tabs do. The
// session cookie is also shared, so the driver sets the cookie of the identity a
// page holds before that page loads (the two identities of the report came from
// two different sign-ins in one browser).
//
// The rule: a stored model restores ONLY when the identity it was stored under
// equals the identity of the SSR seed. Any change of identity restores nothing
// and removes the stored copy; the page boots from the seed.
//
// Usage: node scripts/spa-identity-verify.mjs <backend-app> [--port N]
// Exit: 0 PASS · 2 FAIL · 1 harness error.
import pw from "playwright";
const { chromium } = pw;
import { spawn } from "node:child_process";
import { dirname } from "node:path";

function arg(name, def) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : def;
}
const BACKEND = process.argv[2];
if (!BACKEND) {
  console.error("usage: spa-identity-verify.mjs <backend-app> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9373"));
const BASE = `http://127.0.0.1:${PORT}`;
const BACKEND_DIR = dirname(dirname(BACKEND));
const LEGACY_KEY = "sky:spa:model";

try {
  await fetch(BASE + "/", { signal: AbortSignal.timeout(1000) });
  console.error(`harness error: port ${PORT} is already serving; stop that process first`);
  process.exit(1);
} catch (_) {}

const proc = spawn(BACKEND, [], { cwd: BACKEND_DIR, env: { ...process.env, PORT: String(PORT) } });
let serverLog = "";
proc.stdout.on("data", (d) => (serverLog += d));
proc.stderr.on("data", (d) => (serverLog += d));
const listening = new Promise((res) =>
  proc.stdout.on("data", (d) => d.toString().includes("Sky server listening") && res()),
);

const failures = [];
function check(step, got, want) {
  const ok = got === want;
  console.log(`${ok ? "ok  " : "FAIL"} ${step}: ${JSON.stringify(got)} (want ${JSON.stringify(want)})`);
  if (!ok) failures.push(step);
}

let browser;
try {
  await Promise.race([
    listening,
    new Promise((_, rej) => setTimeout(() => rej(new Error("backend never listened\n" + serverLog)), 20000)),
  ]);
  browser = await chromium.launch({ headless: true });
  const ctx = await browser.newContext();
  const pageErrors = [];
  const open = async () => {
    const p = await ctx.newPage();
    p.on("pageerror", (e) => pageErrors.push(`[pageerror] ${e.message}`));
    return p;
  };
  const A = await open();
  const B = await open();

  const line = async (page, key) => {
    const el = page.locator("#" + key);
    return (await el.count()) ? (await el.innerText()).trim() : `${key}=<absent>`;
  };
  const view = async (page) => [await line(page, "who"), await line(page, "note"), await line(page, "clicks")].join(" ");
  const load = async (page) => {
    await page.goto(BASE + "/", { waitUntil: "networkidle" });
    await page.waitForTimeout(1500); // wasm boot + hydrate
  };
  const reload = async (page) => {
    await page.reload({ waitUntil: "networkidle" });
    await page.waitForTimeout(1500);
  };
  const click = async (page, label, times = 1) => {
    for (let i = 0; i < times; i++) {
      await page.click(`text="${label}"`);
      await page.waitForTimeout(400);
    }
    await page.waitForTimeout(300);
  };
  // The page's localStorage, as every tab of the origin sees it.
  const storage = (page) =>
    page.evaluate(() => {
      const o = {};
      for (let i = 0; i < localStorage.length; i++) {
        const k = localStorage.key(i);
        o[k] = localStorage.getItem(k);
      }
      return o;
    });
  const sid = async () => (await ctx.cookies()).find((c) => c.name === "sky_sid");
  const holdIdentity = async (cookie) => {
    await ctx.clearCookies();
    if (cookie) await ctx.addCookies([cookie]);
  };

  // Tab A: the practitioner signs in and builds scratch state.
  await load(A);
  check("A: first load is signed out", await line(A, "who"), "who=anon");
  await click(A, "signin-practitioner");
  check("A: signed in", await line(A, "who"), "who=practitioner:u1");
  await click(A, "bump", 2);
  await click(A, "note");
  check("A: scratch state", await view(A), "who=practitioner:u1 note=u1-private clicks=2");
  const cookieA = await sid();
  if (!cookieA) throw new Error("sign-in set no sky_sid cookie");

  // Tab B: a signed-out load (another person's browser session) never sees A's state.
  await holdIdentity(null);
  await load(B);
  check("B: a signed-out load shows none of A's state", await view(B), "who=anon note= clicks=0");
  // The patient signs in in tab B and builds scratch state.
  await click(B, "signin-patient");
  check("B: signed in as the patient", await line(B, "who"), "who=patient:p1");
  await click(B, "bump", 5);
  await click(B, "note");
  check("B: scratch state", await view(B), "who=patient:p1 note=p1-private clicks=5");
  const cookieB = await sid();

  // Tab A does a full load (a payment redirect back) under its own identity.
  await holdIdentity(cookieA);
  await reload(A);
  check("A: full load after B wrote shows A's identity and none of B's data", await view(A), "who=practitioner:u1 note= clicks=0");
  const afterA = await storage(A);
  check(
    "A: the patient's stored copy is removed",
    Object.values(afterA).some((v) => v.includes("p1-private")),
    false,
  );
  // A acts again, then reloads: A's own state comes back.
  await click(A, "bump");
  await click(A, "note");
  await reload(A);
  check("A: second full load restores A's own state", await view(A), "who=practitioner:u1 note=u1-private clicks=1");

  // Tab B reloads under the patient's identity: A's model is not restored.
  await holdIdentity(cookieB);
  await reload(B);
  check("B: full load after A wrote shows none of A's data", await view(B), "who=patient:p1 note= clicks=0");

  // Sign-out in A: the stored copy goes, and the next signed-out load starts clean.
  await holdIdentity(cookieA);
  await reload(A);
  await click(A, "bump", 3);
  await click(A, "signout");
  check("A: signed out in the page", await line(A, "who"), "who=anon");
  await click(A, "bump");
  check(
    "A: nothing of the signed-out user stays in storage",
    Object.values(await storage(A)).some((v) => v.includes("u1")),
    false,
  );
  await holdIdentity(null);
  await reload(A);
  check("A: the next signed-out load shows none of the signed-out user's data", await view(A), "who=anon note= clicks=0");

  // Signed out -> signed in: the anonymous state is not carried into the identity.
  await click(A, "bump", 2);
  await click(A, "note");
  check("A: anonymous scratch state", await view(A), "who=anon note=anon-note clicks=2");
  await holdIdentity(cookieA);
  await reload(A);
  check("A: signing in does not restore the anonymous state", await view(A), "who=practitioner:u1 note= clicks=0");
  check(
    "A: the anonymous copy is removed",
    Object.values(await storage(A)).some((v) => v.includes("anon-note")),
    false,
  );

  // A pre-fix blob under the legacy key is never restored, and is deleted.
  await A.evaluate(
    ([k, v]) => localStorage.setItem(k, v),
    [LEGACY_KEY, JSON.stringify({ session: { kind: "practitioner", userId: "u1" }, page: "home", note: "p1-private", clicks: 9 })],
  );
  await reload(A);
  check("A: the legacy blob is not restored", await view(A), "who=practitioner:u1 note= clicks=0");
  check("A: the legacy key is deleted", LEGACY_KEY in (await storage(A)), false);

  for (const m of pageErrors) console.log(m);
  if (pageErrors.length) failures.push("page errors");
  console.log(
    failures.length ? `VERDICT=FAIL ${failures.join("; ")}` : "VERDICT=PASS a stored model restores only for its own identity",
  );
  process.exitCode = failures.length ? 2 : 0;
} catch (e) {
  console.error("HARNESS ERROR:", e.message);
  console.error(serverLog.slice(-2000));
  process.exitCode = 1;
} finally {
  try {
    await browser?.close();
  } catch (_) {}
  proc.kill("SIGKILL");
  process.exit(process.exitCode ?? 1);
}
