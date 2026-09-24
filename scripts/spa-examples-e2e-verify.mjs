#!/usr/bin/env node
// scripts/spa-examples-e2e-verify.mjs
//
// Browser e2e for the Sky.Spa (web:app) examples 62-app-notes and 63-app-chat.
// Driven by scripts/spa-examples-e2e.sh, which builds each example from a
// scratch copy and passes the backend binary here.
//
//   notes  New note → type "Shopping list" → Save → New note → type "Second
//          note" → Save → reload: BOTH titles are in the sidebar (the second
//          Save used to update no row, because Create left `selected = 0`).
//   chat   alice sends a line; a SECOND page then boots and loads the history
//          over the `Load` RPC: the row carries alice's name and text (it used
//          to render with neither — the response record lowered its `messages`
//          field to the stdlib `Std.Ai.Provider.Message`). In development the
//          dev Console badge must not sit over the Send button.
//
// Usage: node scripts/spa-examples-e2e-verify.mjs <notes|chat> <backend-app> [--port N]
// Exit: 0 PASS · 2 FAIL · 1 harness error.
import pw from "playwright";
const { chromium } = pw;
import { spawn } from "node:child_process";
import { dirname, join } from "node:path";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";

function arg(name, def) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : def;
}
const MODE = process.argv[2];
const BACKEND = process.argv[3];
if (!["notes", "chat"].includes(MODE) || !BACKEND) {
  console.error("usage: spa-examples-e2e-verify.mjs <notes|chat> <backend-app> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", MODE === "notes" ? "9341" : "9342"));
const URL = `http://127.0.0.1:${PORT}/`;
const DB = join(mkdtempSync(join(tmpdir(), "sky-spa-examples-")), "app.db");

const proc = spawn(BACKEND, [], {
  cwd: dirname(dirname(BACKEND)),
  // ENV=development: the dev Console badge is on, as a developer sees the app.
  env: { ...process.env, PORT: String(PORT), SKY_DB_PATH: DB, ENV: "development" },
});
let serverLog = "";
proc.stdout.on("data", (d) => (serverLog += d));
proc.stderr.on("data", (d) => (serverLog += d));

const failures = [];
function check(step, ok, detail) {
  console.log(`${ok ? "ok  " : "FAIL"} ${step}${detail ? ": " + detail : ""}`);
  if (!ok) failures.push(step);
}

async function waitListening() {
  for (let i = 0; i < 80; i++) {
    try {
      const r = await fetch(URL);
      if (r.ok) return;
    } catch (_) {}
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error("backend never listened\n" + serverLog);
}

// Wait until the wasm client has hydrated (its first Load RPC answered).
async function openApp(browser) {
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  page.on("pageerror", (e) => failures.push(`[pageerror] ${e.message}`) && console.log(`FAIL [pageerror] ${e.message}`));
  const loaded = page.waitForResponse((r) => r.url().includes("/_rpc/Load"), { timeout: 20000 });
  await page.goto(URL, { waitUntil: "load" });
  await loaded;
  await page.waitForTimeout(400);
  return page;
}

async function rpcDone(page, name, action) {
  const resp = page.waitForResponse((r) => r.url().includes(`/_rpc/${name}`), { timeout: 15000 });
  await action();
  await resp;
  await page.waitForTimeout(400);
}

async function notes(browser) {
  const page = await openApp(browser);
  const title = page.locator("#title-input");
  await rpcDone(page, "Create", () => page.locator("#new-btn").click());
  await title.pressSequentially("Shopping list");
  await rpcDone(page, "Save", () => page.locator("#save-btn").click());
  const afterFirst = await page.locator("#note-list").innerText();
  check("the first Save shows its title in the sidebar at once", afterFirst.includes("Shopping list"), JSON.stringify(afterFirst));

  await rpcDone(page, "Create", () => page.locator("#new-btn").click());
  check("New note clears the editor", (await title.inputValue()) === "", JSON.stringify(await title.inputValue()));
  await title.pressSequentially("Second note");
  await rpcDone(page, "Save", () => page.locator("#save-btn").click());
  const afterSecond = await page.locator("#note-list").innerText();
  check("the second Save shows its title in the sidebar at once", afterSecond.includes("Second note"), JSON.stringify(afterSecond));

  const loaded = page.waitForResponse((r) => r.url().includes("/_rpc/Load"), { timeout: 20000 });
  await page.reload({ waitUntil: "load" });
  await loaded;
  await page.waitForTimeout(400);
  const afterReload = await page.locator("#note-list").innerText();
  check(
    "both notes are persisted across a reload",
    afterReload.includes("Shopping list") && afterReload.includes("Second note"),
    JSON.stringify(afterReload)
  );
}

async function chat(browser) {
  const alice = await openApp(browser);
  await alice.locator("#name-input").fill("alice");
  await alice.locator("#message-input").pressSequentially("hello from alice");

  // The dev badge must not take the Send button's click.
  const send = alice.locator("#send-btn");
  const box = await send.boundingBox();
  const hit = await alice.evaluate(
    ([x, y]) => {
      const el = document.elementFromPoint(x, y);
      return el ? (el.closest("#send-btn") ? "send-btn" : el.id || el.tagName) : "none";
    },
    [box.x + box.width - 4, box.y + box.height - 4]
  );
  check("the dev Console badge does not cover the Send button", hit === "send-btn", hit);

  // Click through the DOM, so a covering badge (checked above) cannot also
  // hide the history check below behind a click timeout.
  await rpcDone(alice, "Send", () => send.evaluate((el) => el.click()));
  await alice.waitForTimeout(800);
  const live = await alice.locator("#messages").innerText();
  check("the sender sees its line (live push)", live.includes("hello from alice"), JSON.stringify(live));

  // A second client boots and loads the HISTORY over the Load RPC.
  const bob = await openApp(browser);
  const history = await bob.locator("#messages").innerText();
  check(
    "a new client's history row carries the author and the text",
    history.includes("alice") && history.includes("hello from alice"),
    JSON.stringify(history)
  );
}

let code = 0;
try {
  await waitListening();
  const browser = await chromium.launch({ headless: true });
  try {
    if (MODE === "notes") await notes(browser);
    else await chat(browser);
  } finally {
    await browser.close();
  }
  if (failures.length) {
    console.log(`\n${MODE}: FAIL — ${failures.length} check(s): ${failures.join("; ")}`);
    console.log("--- server log ---\n" + serverLog.slice(-3000));
    code = 2;
  } else {
    console.log(`\n${MODE}: PASS`);
  }
} catch (e) {
  console.error(`${MODE}: harness error: ${e.stack || e}`);
  console.error("--- server log ---\n" + serverLog.slice(-3000));
  code = 1;
} finally {
  proc.kill("SIGTERM");
}
process.exit(code);
