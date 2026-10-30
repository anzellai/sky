#!/usr/bin/env node
// scripts/spa-typing-hold-verify.mjs
//
// Browser e2e for typing while a slow server answer is in flight (v0.29.0).
//
// Drives rust/crates/sky/tests/fixtures/spa-typing-hold, a Std.App app built
// for --target web:app (Sky.Spa) or --target web (Sky.Live, also what tablet
// and desktop serve), in each browser, under SKY_CSP=strict. Every server
// answer is held for 900 ms by page.route (/_rpc on Sky.Spa, /_sky/event on
// Sky.Live), so the server is slow the way a real search on a loaded server
// is.
//
//   search  a debounced `RunSearch` writes server data into the model, so it
//           is a HOLD: the keystrokes typed while it runs wait behind it. A
//           word typed with 160 ms between keys must end whole, with the
//           caret at its end, and the model must end equal to the box.
//           Before v0.29.0 the render that followed the answer wrote the
//           model's older text into the focused box and clamped the caret,
//           and "clinicare" ended as "clienicar" with the caret at 4.
//   code    the same, through a Std.Html input (`Ui.html (Html.input …)`)
//           instead of Std.Ui.Input: both input paths keep their letters.
//   note    (Sky.Spa only) `SetNote` is hold-free during `SaveNote` (Spa.withHoldFree): its
//           arm writes only `note`, which no hold writes. Typed while the
//           SaveNote request is pending, the note shows at once, and the
//           saved value is the note at the time of the click.
//
// Usage: node scripts/spa-typing-hold-verify.mjs <app-binary> [--port N] [--target web:app|live]
//   SKY_E2E_BROWSERS  comma list of chromium,webkit (default chromium)
//   SKY_E2E_CHANNEL   a Playwright Chromium channel, e.g. "chrome"
//   SKY_E2E_HEADED=1  run the browsers headed
// Exit: 0 PASS · 2 FAIL · 1 harness error.
import pw from "playwright";
import { spawn } from "node:child_process";
import { dirname } from "node:path";
import { guardChild } from "./lib/child-guard.mjs";

function arg(name, def) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : def;
}
const SPA = process.argv[2];
if (!SPA) {
  console.error("usage: spa-typing-hold-verify.mjs <web:app-backend> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9389"));
const TARGET = arg("--target", "web:app");
const LIVE = TARGET === "live";
// The server answers this script delays: Sky.Spa RPCs, or Sky.Live events.
const SERVER_PATH = LIVE ? "/_sky/event" : "/_rpc/";
const BROWSERS = (process.env.SKY_E2E_BROWSERS || "chromium").split(",").map((s) => s.trim()).filter(Boolean);
const HEADED = process.env.SKY_E2E_HEADED === "1";
const CHANNEL = process.env.SKY_E2E_CHANNEL || undefined;
const RPC_DELAY_MS = 900;
const KEY_DELAY_MS = 160;
const WORD = "blueberry";
const CODE = "blackberry";

const failures = [];
function check(step, ok, detail) {
  console.log(`${ok ? "ok  " : "FAIL"} ${step}: ${detail}`);
  if (!ok) failures.push(step);
}

async function until(what, pred, ms = 30000) {
  const deadline = Date.now() + ms;
  while (!(await pred())) {
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 25));
  }
}

async function startApp(bin, port) {
  const env = {
    ...process.env,
    PORT: String(port),
    SKY_LIVE_PORT: String(port),
    SKY_CSP: "strict",
  };
  const proc = guardChild(spawn(bin, [], { cwd: dirname(dirname(bin)), env }));
  let log = "";
  proc.stdout.on("data", (d) => (log += d));
  proc.stderr.on("data", (d) => (log += d));
  const deadline = Date.now() + 30000;
  while (!/listening/i.test(log)) {
    if (Date.now() > deadline || proc.exitCode !== null) throw new Error(`${bin} never listened:\n${log}`);
    await new Promise((r) => setTimeout(r, 100));
  }
  return { proc, log: () => log };
}

async function launch(name) {
  if (name === "webkit") return pw.webkit.launch({ headless: !HEADED });
  return pw.chromium.launch({ headless: !HEADED, channel: CHANNEL });
}

const text = async (page, id) => (await page.locator("#" + id).innerText()).trim();
const box = (page, id) =>
  page.locator("#" + id).evaluate((el) => ({ value: el.value, caret: el.selectionStart }));
// Two animation frames: the page has rendered every Msg it has run.
const settled = (page) =>
  page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));

async function scenario(browserName, url) {
  const browser = await launch(browserName);
  const tag = `${TARGET}/${browserName}`;
  const page = await browser.newPage();
  const errors = [];
  const rpc = { sent: [], done: new Set() };
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => {
    const where = m.location()?.url || "";
    if (m.type() === "error" && !/favicon/.test(where)) errors.push(m.text());
  });
  page.on("request", (r) => {
    if (r.url().includes(SERVER_PATH)) rpc.sent.push(r);
  });
  page.on("requestfinished", (r) => rpc.done.add(r));
  page.on("requestfailed", (r) => rpc.done.add(r));
  const pending = (path) => rpc.sent.some((r) => r.url().includes(path) && !rpc.done.has(r));
  const anyPending = () => rpc.sent.some((r) => !rpc.done.has(r));
  // Quiet: no request pending and none sent for 1.5 s. A Sub.every tick that
  // fired while the last search ran waits behind it and searches once more
  // after it (its own hold), so "nothing pending" alone can be read in the
  // gap before that request goes out.
  let lastSent = Date.now();
  page.on("request", (r) => {
    if (r.url().includes(SERVER_PATH)) lastSent = Date.now();
  });
  const quiet = () => !anyPending() && Date.now() - lastSent > 1500;
  // Every server branch answers 900 ms late.
  await page.route(LIVE ? "**/_sky/event**" : "**/_rpc/**", async (route) => {
    await new Promise((r) => setTimeout(r, RPC_DELAY_MS));
    await route.continue();
  });
  try {
    await page.goto(url, { waitUntil: "load" });
    await until(`${tag} first paint`, async () => (await text(page, "query-echo")) === "QUERY=");
    await settled(page);

    // ---- search (Std.Ui) and code (Std.Html): typed against a slow server ----
    for (const [label, id, echoId, hitsId, echoKey, hitsKey, word, rpcName] of [
      ["search", "query", "query-echo", "hits", "QUERY", "HITS", WORD, "RunSearch"],
      ["code (Std.Html)", "code", "code-echo", "code-hits", "CODE", "CODEHITS", CODE, "RunLookup"],
    ]) {
      await page.locator("#" + id).click();
      // One key every 160 ms, counting the keys typed while a server answer
      // was still held: the test means something only when they overlap.
      let overlapped = 0;
      for (const ch of word) {
        if (anyPending()) overlapped++;
        await page.keyboard.type(ch);
        await new Promise((r) => setTimeout(r, KEY_DELAY_MS));
      }
      check(`${tag} ${label}: keys were typed while a server answer was held`, overlapped >= 3, `keys typed while pending=${overlapped}`);
      if (!LIVE) {
        const searches = rpc.sent.filter((r) => r.url().includes("/_rpc/" + rpcName)).length;
        check(`${tag} ${label}: the debounced search ran while typing (a hold)`, searches >= 1, `${rpcName} requests=${searches}`);
      }
      await until(
        `${tag} ${label}: the searches settle`,
        async () => quiet() && (await text(page, hitsId)) === `${hitsKey}=${word}`,
      ).catch(() => {});
      await settled(page);
      const q = await box(page, id);
      check(`${tag} ${label}: the word is whole`, q.value === word, JSON.stringify(q));
      check(`${tag} ${label}: the caret is at the end`, q.caret === word.length, JSON.stringify(q));
      const echo = await text(page, echoId);
      check(`${tag} ${label}: the model ends equal to the box`, echo === `${echoKey}=${q.value}`, echo);
      const hits = await text(page, hitsId);
      check(`${tag} ${label}: the last search is for the whole word`, hits === `${hitsKey}=${word}`, hits);
    }

    // ---- note: a hold-free input runs during the hold (Sky.Spa) --------------
    // Sky.Live has no hold: every event runs on the server in order, so the
    // typed note shows when its own answer arrives.
    if (LIVE) {
      check(`${tag} no page errors`, errors.length === 0, errors.join(" | ") || "none");
      return;
    }
    await page.click("#save");
    await until(`${tag} the SaveNote request`, async () => pending("/_rpc/SaveNote"));
    await page.locator("#note").click();
    await page.locator("#note").pressSequentially("ok", { delay: 30 });
    await settled(page);
    const during = await text(page, "note-echo");
    const stillPending = pending("/_rpc/SaveNote");
    check(`${tag} note: SaveNote is still pending while the note is read`, stillPending, `pending=${stillPending}`);
    check(`${tag} note: the typed note shows during the hold`, during === "NOTE=ok", during);
    await until(`${tag} the save answer`, async () => quiet()).catch(() => {});
    await until(`${tag} the saved value`, async () => (await text(page, "saved")) === "SAVED=saved:").catch(() => {});
    await settled(page);
    const saved = await text(page, "saved");
    check(`${tag} note: the save holds the note of the click`, saved === "SAVED=saved:", saved);
    const n = await box(page, "note");
    check(`${tag} note: the note box keeps its text and caret`, n.value === "ok" && n.caret === 2, JSON.stringify(n));
    check(`${tag} note: the model ends equal to the box`, (await text(page, "note-echo")) === "NOTE=ok", await text(page, "note-echo"));
    check(`${tag} no page errors`, errors.length === 0, errors.join(" | ") || "none");
  } finally {
    await browser.close();
  }
}

let app;
try {
  app = await startApp(SPA, PORT);
  for (const b of BROWSERS) {
    await scenario(b, `http://127.0.0.1:${PORT}/`);
  }
  console.log(
    failures.length
      ? `VERDICT=FAIL ${failures.join("; ")}`
      : `VERDICT=PASS (${TARGET}) typing against a slow server keeps every letter and the caret${LIVE ? "" : "; a hold-free input runs at once"}`,
  );
  process.exitCode = failures.length ? 2 : 0;
} catch (e) {
  console.error("HARNESS ERROR:", e.message);
  if (app) console.error(app.log().slice(-2000));
  process.exitCode = 1;
} finally {
  if (app) app.proc.kill("SIGKILL");
  process.exit(process.exitCode ?? 1);
}
