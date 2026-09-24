#!/usr/bin/env node
// scripts/spa-vdom-identity-verify.mjs
//
// Browser e2e for the Sky.Spa DOM driver: node identity across the shared
// diff, the form-control payloads, the Elm rule for refused input, IME, labels,
// injected styles, hydration parity and route-param decoding. Drives the
// fixture rust/crates/sky/tests/fixtures/spa-vdom-identity (see its header for
// the finding each control pins) in real headless Chromium.
//
// With --live the app is the same fixture built for Sky.Live (--target web),
// and only the checks that exercise the SHARED diff and the stdlib run: node
// identity (F8, K2), select value (F4), injected styles (F6) and labels
// (UF-13). The Live client's own payload / input-authority / IME handling is covered
// by the Sky.Live suites.
//
// Usage: node scripts/spa-vdom-identity-verify.mjs <app> [--port N] [--live]
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
  console.error("usage: spa-vdom-identity-verify.mjs <backend-app> [--port N]");
  process.exit(1);
}
const PORT = Number(arg("--port", "9200"));
const BASE = `http://127.0.0.1:${PORT}`;
const BACKEND_DIR = dirname(dirname(BACKEND));
const LIVE = process.argv.includes("--live");

const proc = spawn(BACKEND, [], { cwd: BACKEND_DIR, env: { ...process.env, PORT: String(PORT), SKY_LIVE_PORT: String(PORT) } });
let serverLog = "";
proc.stdout.on("data", (d) => (serverLog += d));
proc.stderr.on("data", (d) => (serverLog += d));
const listening = new Promise((res) => proc.stdout.on("data", (d) => /listening/i.test(d.toString()) && res()));

const failures = [];
function check(ok, step, detail) {
  console.log(`${ok ? "ok  " : "FAIL"} ${step}${detail ? " — " + detail : ""}`);
  if (!ok) failures.push(step);
}

function watch(page, sink) {
  page.on("pageerror", (e) => sink.push(`[pageerror] ${e.message}`));
  page.on("console", (m) => {
    const t = m.text();
    if ((t.includes("[sky.spa]") || t.includes("[sky.live]")) && (m.type() === "warning" || m.type() === "error")) sink.push(`[${m.type()}] ${t}`);
  });
}

const text = (page, sel) => page.locator(sel).first().innerText();
const logText = (page) => text(page, "#log");
const stateText = (page) => text(page, "#state");
// Sky.Live round-trips every event (and debounces typing), so it settles slower.
const settle = (page) => page.waitForTimeout(LIVE ? 700 : 150);

async function boot(page, path = "/") {
  await page.goto(BASE + path, { waitUntil: LIVE ? "load" : "networkidle" });
  await page.waitForFunction(() => !document.documentElement.hasAttribute("data-sky-hydrating"), null, { timeout: 15000 });
  await page.waitForTimeout(300);
}

let browser;
try {
  await Promise.race([
    listening,
    new Promise((_, rej) => setTimeout(() => rej(new Error("backend never listened\n" + serverLog)), 20000)),
  ]);
  browser = await chromium.launch({ headless: true });

  // ── Home: identity, payloads, reconcile, labels, IME, styles ─────────
  {
    const page = await browser.newPage();
    const warn = [];
    watch(page, warn);
    const released = [];
    page.on("console", (m) => /released function/i.test(m.text()) && released.push(m.text()));
    await boot(page);

    check((await page.locator("#sel").inputValue()) === "c", "F5 select first paint shows the model value");

    // F8 — a sibling line appears above the field mid-typing.
    await page.locator("#vv").click();
    await page.keyboard.type("abcdefg", { delay: LIVE ? 120 : 40 });
    await settle(page);
    const vv = await page.locator("#vv").inputValue();
    const focused = await page.evaluate(() => document.activeElement && document.activeElement.id === "vv");
    const st = await stateText(page);
    check(vv === "abcdefg" && st.includes("vv=abcdefg") && focused,
      "F8 sibling insert above a focused input loses no keystroke and keeps focus",
      `value=${vv} focused=${focused} state=${st.split(" ")[0]}`);

    // K2 — the keyed row's key follows the model.
    await page.locator("#b-rekey").click();
    await settle(page);
    await page.locator("#kbtn").click();
    await settle(page);
    const lg = await logText(page);
    check(lg.endsWith("PickK:b"), "K2 a re-keyed row dispatches its new message", `log=${lg}`);

    // F4 — an option prepended before the chosen one.
    await page.locator("#b-addopt").click();
    await settle(page);
    check((await page.locator("#sel").inputValue()) === "c", "F4 select keeps the model value when options change");

    if (!LIVE) {
    // F10 — key payload.
    await page.locator("#kd").click();
    await page.keyboard.press("x");
    await settle(page);
    check((await logText(page)).includes("Key:x"), "F10 onKeyDown receives event.key");

    // F11 — onCheck gets a Bool.
    await page.locator("#ck").click();
    await settle(page);
    check((await logText(page)).includes("Chk:T"), "F11 onCheck receives the checked Bool");

    // Elm rule (UF-5 by design): the DOM is written only when the rendered
    // value CHANGES. update refuses input past 3 characters, so the model and
    // the render stay "abc" and the field keeps every keystroke the user made.
    await page.locator("#lim").click();
    await page.keyboard.type("abcde", { delay: 30 });
    await settle(page);
    const lim = await page.locator("#lim").inputValue();
    check(lim === "abcde" && (await stateText(page)).includes("lim=abc accept="),
      "Elm rule: a refused edit leaves the field as the user typed it", `value=${lim}`);
    await page.locator("#b-limset").click();
    await settle(page);
    const limSet = await page.locator("#lim").inputValue();
    check(limSet === "xyz", "Elm rule: a model value that changes is applied to the field", `value=${limSet}`);
    const lockBox = page.locator("#lock input[type=checkbox]");
    await lockBox.click();
    await settle(page);
    const lockChecked = await lockBox.isChecked();
    check(lockChecked && (await logText(page)).includes("SetLock"),
      "Elm rule: an ignored tick leaves the checkbox as the user left it", `checked=${lockChecked}`);
    await page.locator("#b-lockflip").click();  // model False -> True
    await settle(page);
    await page.locator("#b-lockflip").click();  // model True -> False: checked removed
    await settle(page);
    const lockAfter = await lockBox.isChecked();
    check(!lockAfter, "Elm rule: a checked value that changes is applied to the checkbox", `checked=${lockAfter}`);

    }

    // UF-13 — the caption text is a real label.
    await page.getByText("Accept terms", { exact: true }).click();
    await settle(page);
    check((await stateText(page)).includes("accept=T"), "UF-13 clicking a checkbox caption ticks it");
    await page.getByText("Beta", { exact: true }).click();
    await settle(page);
    check((await stateText(page)).includes("colour=b"), "UF-13 clicking a radio option's text picks it");

    if (!LIVE) {
    // UF-11 — IME: pre-edit is not dispatched, the committed text is, once.
    await page.locator("#ime").click();
    const cdp = await page.context().newCDPSession(page);
    await cdp.send("Input.imeSetComposition", { text: "k", selectionStart: 1, selectionEnd: 1 });
    await cdp.send("Input.imeSetComposition", { text: "か", selectionStart: 1, selectionEnd: 1 });
    await cdp.send("Input.insertText", { text: "か" });
    await settle(page);
    const imeLog = (await logText(page)).split(",").filter((s) => s.startsWith("Ime:"));
    const imeVal = await page.locator("#ime").inputValue();
    check(imeLog.length === 1 && imeLog[0] === "Ime:か" && imeVal === "か",
      "UF-11 IME pre-edit is not dispatched; the committed text is, once", `dispatched=${JSON.stringify(imeLog)} value=${imeVal}`);

    }

    // W1 — rows appear after an RPC reply in the slot of a placeholder button
    // whose handler differs; the diff keeps that node and rebinds it. A row
    // button pressed afterwards must dispatch, with no released js.Func left
    // attached ("call to released function" on every click).
    await page.locator("#b-loadrows").click();
    await page.locator("#row-b").waitFor({ timeout: 10000 });
    await settle(page);
    await page.locator("#row-a").click();
    await settle(page);
    await page.locator("#row-b").click();
    await settle(page);
    const rowLog = await logText(page);
    check(rowLog.includes("OpenRow:a") && rowLog.includes("OpenRow:b") && released.length === 0,
      "W1 a row button that appeared after an RPC reply dispatches with no released-function call",
      `log=${rowLog.slice(0, 80)} released=${released.length}`);

    // F6 — the injected hover style follows the model.
    const hovCss = () => page.evaluate(() => {
      const id = document.querySelector("#hov").getAttribute("sky-id");
      const st = [...document.querySelectorAll("style[data-sky-pc]")].find((s) => s.getAttribute("data-sky-pc") === id);
      return st ? st.textContent : "";
    });
    const before = await hovCss();
    await page.locator("#b-toggle").click();
    await settle(page);
    const after = await hovCss();
    check(before !== "" && after !== "" && before !== after, "F6 the injected hover <style> follows the model");

    if (!LIVE) {
    // SPA-5 — client-side navigation to a non-ASCII route param.
    await page.evaluate(() => {
      const a = document.createElement("a");
      a.href = "/u/J%C3%B6rg";
      a.id = "go-user";
      a.textContent = "user";
      document.body.appendChild(a);
    });
    await page.locator("#go-user").click();
    await settle(page);
    const nav = `${await text(page, "#uname")} ${await text(page, "#ulen")}`;
    check(nav === "name=Jörg len=4", "SPA-5 client navigation decodes the route param like the server", nav);
    }

    check(warn.length === 0, "no [sky.spa] warnings or page errors on Home", warn.join(" | "));
    await page.close();
  }

  // ── SPA-5: cold load of the route — server and client agree ──────────
  if (!LIVE) {
    const page = await browser.newPage();
    const warn = [];
    watch(page, warn);
    await boot(page, "/u/J%C3%B6rg");
    const cold = `${await text(page, "#uname")} ${await text(page, "#ulen")}`;
    check(cold === "name=Jörg len=4", "SPA-5 cold load shows the decoded route param", cold);
    check(!warn.some((w) => w.includes("hydrate skipped")), "SPA-5 cold load hydrates (server and client first views agree)", warn.join(" | "));
    await page.close();
  }

  // ── Back / Forward (popstate) runs onNavigate — both targets ─────────
  // Sky.Spa fires onNavigate on the client; Sky.Live fires it on the server
  // for the nav GET the popstate makes. Either way the app sees one Msg per
  // step and shows the page of the URL.
  {
    const page = await browser.newPage();
    await boot(page, "/u/a");
    const navsOf = async () => (await text(page, "#navs")).split(",").filter((s) => s !== "");
    const before = await navsOf();
    check(JSON.stringify(before) === JSON.stringify(["u:a"]),
      "onNavigate runs once for the first paint of a route", JSON.stringify(before));
    await page.evaluate(() => history.pushState({}, "", "/u/b"));
    await page.evaluate(() => history.back()); // popstate → /u/a
    await page.waitForTimeout(LIVE ? 900 : 400);
    await page.evaluate(() => history.forward()); // popstate → /u/b
    await page.waitForTimeout(LIVE ? 900 : 400);
    const shown = await text(page, "#uname");
    const after = await navsOf();
    check(shown === "name=b" && after.length === before.length + 2 &&
      JSON.stringify(after.slice(-2)) === JSON.stringify(["u:a", "u:b"]),
      "popstate routes the page and runs onNavigate once per Back / Forward",
      `page=${shown} before=${JSON.stringify(before)} after=${JSON.stringify(after)}`);
    await page.close();
  }

  // ── Hydration of a typical Std.Ui page (register M) ───────────────────
  // /plain holds adjacent text runs (the server writes them back to back, so
  // the browser parses ONE text node where the client tree holds several), a
  // Ui.paragraph with a link, a text input and a valued textarea. Each of
  // these used to make the client refuse the SSR DOM ("hydrate skipped, full
  // rebuild: adjacent text" / "textarea value") and rebuild the page. A marker
  // set on the server nodes BEFORE the wasm boots must survive the boot.
  if (!LIVE) {
    const page = await browser.newPage();
    const warn = [];
    watch(page, warn);
    await page.route(BASE + "/plain", async (route) => {
      const resp = await route.fetch();
      const mark = `<script>for (const s of ["#hy-p", "#hy-para", "#hy-in", "#notes"]) { const n = document.querySelector(s); if (n) n.__skySsr = 1; }` +
        `const p = document.querySelector("#hy-p"); if (p && p.firstChild) p.firstChild.__skySsr = 1;</script>`;
      const body = (await resp.text()).replace(`<script src="/wasm_exec.js">`, mark + `<script src="/wasm_exec.js">`);
      await route.fulfill({ response: resp, body });
    });
    await boot(page, "/plain");
    check(!warn.some((w) => w.includes("hydrate skipped")), "a page with adjacent text, a paragraph, inputs and a valued textarea hydrates", warn.join(" | "));
    const kept = await page.evaluate(() => {
      const p = document.querySelector("#hy-p");
      return {
        els: ["#hy-p", "#hy-para", "#hy-in", "#notes"].every((s) => document.querySelector(s)?.__skySsr === 1),
        text: p?.firstChild?.__skySsr === 1,
        nodes: p ? p.childNodes.length : -1,
      };
    });
    check(kept.els && kept.text, "hydration keeps the server-rendered element and text nodes", JSON.stringify(kept));
    check(kept.nodes === 4, "a server text run is split into the client's text nodes", `childNodes=${kept.nodes}`);
    check((await text(page, "#hy-p")) === "Hello, a! dark=no", "the split text run shows the server text", await text(page, "#hy-p"));
    check((await page.locator("#notes").inputValue()) === "notes", "a hydrated valued textarea shows its value");
    check((await page.locator("#sel2").inputValue()) === "c", "F5 select first paint on the hydrate path shows the model value");
    await page.locator("#b-toggle").click();
    await settle(page);
    check((await text(page, "#hy-p")) === "Hello, a! dark=yes", "a hydrated text run follows the model", await text(page, "#hy-p"));
    await page.locator("#hy-in").fill("Ada");
    await settle(page);
    check((await page.locator("#hy-in").inputValue()) === "Ada", "a hydrated text input keeps what the user types");
    check(warn.length === 0, "no [sky.spa] warnings or page errors on /plain", warn.join(" | "));
    await page.close();
  }

  // ── Hydration parity: a server page that differs is rebuilt ──────────
  if (!LIVE) {
    const page = await browser.newPage();
    const warn = [];
    watch(page, warn);
    await page.route(BASE + "/", async (route) => {
      const resp = await route.fetch();
      const body = (await resp.text()).replace("Accept terms", "Tampered text");
      await route.fulfill({ response: resp, body });
    });
    await boot(page);
    const body = await page.locator("body").innerText();
    check(body.includes("Accept terms") && !body.includes("Tampered text"),
      "SPA-5 hydration verifies text parity and rebuilds a server DOM that differs");
    check((await page.locator("#sel").inputValue()) === "c", "F5 select first paint on the rebuild path shows the model value");
    await page.close();
  }
  if (failures.length) {
    console.error(`\nspa-vdom-identity: ${failures.length} FAILED: ${failures.join("; ")}`);
    process.exitCode = 2;
  } else {
    console.log("\nspa-vdom-identity: all checks PASS");
    process.exitCode = 0;
  }
} catch (e) {
  console.error("harness error:", e);
  process.exitCode = 1;
} finally {
  // A hung browser or app must not leave the gate running: close both and
  // exit explicitly on every path.
  try {
    await browser?.close();
  } catch (_) {}
  proc.kill("SIGKILL");
  process.exit(process.exitCode ?? 1);
}
