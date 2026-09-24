#!/usr/bin/env node
// scripts/spa-reload-verify.mjs
//
// Browser e2e for the Sky.Spa (web:app) full-load rule (register M, R2/R3).
// Drives rust/crates/sky/tests/fixtures/spa-rc-reload in headless Chromium.
// Run by scripts/spa-restore-e2e.sh.
//
// On a full page load the client restores its stored model and takes from the
// SSR seed ONLY the fields the server settled for THIS page (the init and
// onNavigate command chains it finished). Every other field keeps the stored
// value. Each check is what Sky.Live shows for the same session:
//
//   R2   /settings has no loader: `me` (written only by a server branch) is
//        restored, not `init`'s "" ("settings=loading" forever).
//   R2a  the basket (written only by a server branch) survives a full load.
//   R2b  /admin's onNavigate loads the products on the server: a full load
//        shows them even though the stored copy is empty; a reload after a
//        client edit shows the server's products again (the seed wins).
//   R2c  onNavigate clears the notice / error banners: they do not come back
//        on a full load.
//   init init's command wrote `config`: a changed server value wins.
//   R3   a wrong password keeps the page and shows the error.
//
// Usage: node scripts/spa-reload-verify.mjs <backend-app> [--port N]
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
  console.error("usage: spa-reload-verify.mjs <backend-app> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9372"));
const BASE = `http://127.0.0.1:${PORT}`;
const BACKEND_DIR = dirname(dirname(BACKEND));

try {
  await fetch(BASE + "/", { signal: AbortSignal.timeout(1000) });
  console.error(`harness error: port ${PORT} is already serving; stop that process first`);
  process.exit(1);
} catch (_) {}

writeFileSync(join(BACKEND_DIR, "data", "config.txt"), "v1\n");

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
  const page = await browser.newPage();
  const pageErrors = [];
  page.on("pageerror", (e) => pageErrors.push(`[pageerror] ${e.message}`));
  let rpcs = [];
  page.on("request", (r) => r.url().includes("/_rpc") && rpcs.push(r.url()));

  // One line of the view, by its element id.
  const line = async (key) => {
    const el = page.locator("#" + key);
    return (await el.count()) ? (await el.innerText()).trim() : `${key}=<absent>`;
  };
  const load = async (path) => {
    await page.goto(BASE + path, { waitUntil: "networkidle" });
    await page.waitForTimeout(1500); // wasm boot + hydrate (+ any boot RPC)
  };
  const click = async (label) => {
    await page.click(`text=${label}`);
    await page.waitForTimeout(700);
  };

  // A signed-in session with a basket, a notice and an empty product list.
  await load("/");
  check("boot: init's read settled", await line("config"), "config=v1");
  await click("signin-good");
  check("sign-in: notice shown", await line("notice"), "notice=Welcome back");
  await click("add-apple");
  await click("add-pear");
  check("basket filled", await line("basket"), "basket=apple,pear");
  await click("bump");

  // The server's init data changes behind the client's back.
  writeFileSync(join(BACKEND_DIR, "data", "config.txt"), "v2\n");

  // /settings: no loader. Server-only data is restored, not reset.
  await load("/settings");
  check("R2: /settings restores data only a server branch wrote", await line("settings"), "settings=ada");
  check("R2a: the basket survives a full load", await line("basket"), "basket=apple,pear");
  check("R2c: onNavigate cleared the notice on a full load", await line("notice"), "notice=");
  check("init: the server's new init data wins", await line("config"), "config=v2");
  check("client scratch state restored", await line("clicks"), "clicks=1");

  // /admin: onNavigate loads the products on the server.
  await load("/admin");
  check("R2b: /admin shows the server-loaded products", await line("products"), "products=p1,p2,p3");
  check("R2a: the basket survives a second full load", await line("basket"), "basket=apple,pear");
  await click("clear-products");
  check("client edit empties the list", await line("products"), "products=none");
  rpcs = [];
  await load("/admin");
  check("R2b: a reload of an SSR-loaded page takes the seed", await line("products"), "products=p1,p2,p3");
  check("R2b: the settled page makes no second load", rpcs.length, 0);

  // R3: a wrong password keeps the page.
  await load("/signin");
  check("R3: on the sign-in page", await line("page"), "page=signin");
  await click("signin-bad");
  check("R3: a wrong password shows the error", await line("error"), "error=wrong password");
  check("R3: a wrong password keeps the page", await line("page"), "page=signin");
  check("R3: the address stays /signin", new URL(page.url()).pathname, "/signin");

  for (const m of pageErrors) console.log(m);
  if (pageErrors.length) failures.push("page errors");
  console.log(failures.length ? `VERDICT=FAIL ${failures.join("; ")}` : "VERDICT=PASS full loads restore like Sky.Live");
  process.exitCode = failures.length ? 2 : 0;
} catch (e) {
  console.error("HARNESS ERROR:", e.message);
  console.error(serverLog.slice(-2000));
  process.exitCode = 1;
} finally {
  // An error would leave Chromium open and Node would never exit: close it,
  // stop the app, exit explicitly.
  try {
    await browser?.close();
  } catch (_) {}
  proc.kill("SIGKILL");
  process.exit(process.exitCode ?? 1);
}
