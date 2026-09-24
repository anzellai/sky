#!/usr/bin/env node
// scripts/spa-vdom-identity-verify.mjs
//
// Browser e2e for the Sky.Spa DOM driver: node identity across the shared
// diff, the form-control payloads, the user-event reconcile, IME, labels,
// injected styles, hydration parity and route-param decoding. Drives the
// fixture rust/crates/sky/tests/fixtures/spa-vdom-identity (see its header for
// the finding each control pins) in real headless Chromium.
//
// With --live the app is the same fixture built for Sky.Live (--target web),
// and only the checks that exercise the SHARED diff and the stdlib run: node
// identity (F8, K2), select value (F4), injected styles (F6) and labels
// (UF-13). The Live client's own payload / reconcile / IME handling is covered
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

    // UF-5 — update refuses input.
    await page.locator("#lim").click();
    await page.keyboard.type("abcde", { delay: 30 });
    await settle(page);
    const lim = await page.locator("#lim").inputValue();
    check(lim === "abc", "UF-5 a text field shows the model when update refuses input", `value=${lim}`);
    await page.locator("#lock input[type=checkbox]").click();
    await settle(page);
    const lockChecked = await page.locator("#lock input[type=checkbox]").isChecked();
    check(!lockChecked && (await logText(page)).includes("SetLock"),
      "UF-5 a checkbox stays unticked when update refuses the tick", `checked=${lockChecked}`);

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

  // ── F5: a page the client builds from scratch (not hydratable) ───────
  if (!LIVE) {
    const page = await browser.newPage();
    await boot(page, "/plain");
    check((await page.locator("#sel2").inputValue()) === "c", "F5 select first paint on the build-from-scratch path shows the model value");
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
