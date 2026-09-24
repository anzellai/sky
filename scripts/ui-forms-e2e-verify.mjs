#!/usr/bin/env node
// scripts/ui-forms-e2e-verify.mjs
//
// Browser e2e for three "compiles, then fails at run time" holes, driven on the
// fixture rust/crates/sky/tests/fixtures/ui-forms-e2e in headless Chromium:
//
//   1. Ui.onKeyDown — the view used to crash on EVERY render (Sky.Live showed
//      "Render error rt.Coerce …", the Sky.Spa page stayed blank). Keydowns
//      must now count.
//   2. Ui.onSubmit into a typed record — "42" used to arrive as 0 and a checked
//      box as False. The record must arrive decoded (the fixture adds 1 to age,
//      so 42 shows as 43), and a form whose Int field does not parse must be
//      DROPPED (no Msg), never delivered with a zero.
//   3. Literal-topic pub/sub (Sky.Live only) — a publish on the topic reaches
//      the subscriber.
//
// Usage: node scripts/ui-forms-e2e-verify.mjs <app> --mode live|spa [--port N]
// Exit: 0 PASS · 2 FAIL · 1 harness error.
import pw from "playwright";
const { chromium } = pw;
import { spawn } from "node:child_process";
import { dirname } from "node:path";

function arg(name, def) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : def;
}
const APP = process.argv[2];
const MODE = arg("--mode", "live");
if (!APP || !["live", "spa"].includes(MODE)) {
  console.error("usage: ui-forms-e2e-verify.mjs <app> --mode live|spa [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9260"));
const APP_DIR = dirname(dirname(APP));

const env = { ...process.env, PORT: String(PORT), SKY_LIVE_PORT: String(PORT) };
const proc = spawn(APP, [], { cwd: APP_DIR, env });
let serverLog = "";
proc.stdout.on("data", (d) => (serverLog += d));
proc.stderr.on("data", (d) => (serverLog += d));

async function waitListening() {
  const deadline = Date.now() + 30000;
  while (Date.now() < deadline) {
    try {
      await fetch(`http://127.0.0.1:${PORT}/`);
      return;
    } catch (_) {}
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error("app never listened\n" + serverLog);
}

const failures = [];
function check(ok, step, detail) {
  console.log(`${ok ? "ok  " : "FAIL"} [${MODE}] ${step}${detail ? ": " + detail : ""}`);
  if (!ok) failures.push(step);
}
async function text(page, id) {
  const loc = page.locator(`#${id}`);
  if ((await loc.count()) === 0) return "<missing>";
  return (await loc.first().innerText()).trim();
}

let browser;
try {
  await waitListening();
  browser = await chromium.launch({ headless: true });
  const page = await browser.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(`[pageerror] ${e.message}`));
  page.on("console", (m) => m.type() === "error" && errors.push(`[console] ${m.text()}`));

  // Both clients hold an SSE stream open (Sky.Live, and the Sky.Spa topic
  // subscription), so "networkidle" never settles.
  await page.goto(`http://127.0.0.1:${PORT}/`, { waitUntil: "load" });
  await page.waitForTimeout(MODE === "spa" ? 3000 : 500);

  // 1. The view renders at all (UF-1 crashed it on every render).
  const body = await page.locator("body").innerText();
  check(!/Render error/i.test(body) && (await text(page, "keys")) === "keys:0", "view renders with Ui.onKeyDown",
    (await text(page, "keys")) + (/Render error/i.test(body) ? " (Render error on page)" : ""));

  // 2. Keydowns dispatch.
  if ((await page.locator("#keybox").count()) > 0) {
    await page.locator("#keybox").first().click();
    await page.keyboard.press("a");
    await page.keyboard.press("b");
    await page.waitForTimeout(600);
  }
  const keys = await text(page, "keys");
  check(/^keys:[1-9]/.test(keys), "Ui.onKeyDown dispatches on keydown", keys);

  // 3. A typed form decodes Int / Bool / Maybe.
  if ((await page.locator("#ft-title").count()) > 0) {
    await page.locator("#ft-title").first().fill("t");
    await page.locator("#ft-age").first().fill("42");
    await page.locator("#ft-agree").first().check();
    await page.locator("#ft-go").first().click();
    await page.waitForTimeout(800);
  }
  const want = "submitted:title=t age=43 agree=T note=nothing";
  const got = await text(page, "submitted");
  check(got === want, "typed onSubmit decodes 42 / checked / empty Maybe", `${got} (want ${want})`);

  // 4. A form whose Int does not parse is dropped, not zero-filled.
  if ((await page.locator("#fb-title").count()) > 0) {
    await page.locator("#fb-title").first().fill("x");
    await page.locator("#fb-age").first().fill("forty");
    await page.locator("#fb-go").first().click();
    await page.waitForTimeout(800);
  }
  const submits = await text(page, "submits");
  const after = await text(page, "submitted");
  check(submits === "submits:1" && after === want, "unparsable Int field drops the submit", `${submits} / ${after}`);
  if (MODE === "live") {
    check(/FormDecode/.test(serverLog), "the dropped submit is logged as a FormDecode error");
  } else {
    check(errors.some((e) => /FormDecode/.test(e)), "the dropped submit is logged as a FormDecode error",
      errors.join(" | ").slice(0, 300));
  }

  // 5. Literal-topic pub/sub (Sky.Live).
  if (MODE === "live") {
    await page.locator("#pub").first().click();
    await page.waitForTimeout(1000);
    const chat = await text(page, "chat");
    check(chat === "chat:hello-topic", "literal-topic publish reaches the subscriber", chat);
  }
} catch (e) {
  console.error(e.stack || String(e));
  failures.push("harness");
} finally {
  if (browser) await browser.close();
  proc.kill("SIGTERM");
}
if (failures.length) {
  console.log(`FAIL [${MODE}]: ${failures.join(", ")}`);
  console.log("--- app log (tail) ---\n" + serverLog.split("\n").slice(-30).map((l) => l.slice(0, 300)).join("\n"));
  process.exit(failures.includes("harness") ? 1 : 2);
}
console.log(`PASS [${MODE}]`);
