#!/usr/bin/env node
// scripts/verify-script-revival-allowlist.mjs
//
// Cycle 3 audit gap C9 / cycle 2 plan P31 — Playwright probe for
// the __skyReviveScripts XSS hardening.
//
// What it does: boots a one-off Sky.Live demo app (the
// 09-live-counter example), extracts the live runtime JS by
// fetching the initial page, then drives a fresh blank page in
// Chromium where the JS is injected verbatim. The probe mounts
// THREE different malicious script patterns inside an element
// scoped as sky-root, calls __skyReviveScripts, and asserts:
//
//   - No malicious callback fired (no window.__pwned set).
//   - The malicious onerror / onload / onclick attribute is
//     STRIPPED from the revived <script>.
//   - Inline <script>alert(1)</script> (no src) is dropped.
//   - Legitimate <script src="..."> revival STILL works
//     (regression for sky-editor's Editor.scriptTag).
//
// The probe runs entirely inside the Chromium page — no app
// process needed once liveJS() is extracted. This keeps it
// independent of the Sky-app lifecycle.
//
// Usage: node scripts/verify-script-revival-allowlist.mjs
//
// Exit codes:
//   0 — all probes PASS
//   1 — one or more probes FAILED (attack landed)

import { chromium } from "playwright";
import { spawn, spawnSync } from "node:child_process";
import { mkdirSync, writeFileSync, existsSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const ROOT = resolve(__dirname, "..");

// ─── 1. Extract liveJS() output from a one-off Sky boot ────────
//
// The cheap path: spawn the 09-live-counter example, GET / once,
// fetch the external client script the page loads.
// We then kill the app; no further app interaction needed.

const EXAMPLE_DIR = join(ROOT, "examples", "09-live-counter");
const APP_BIN = join(EXAMPLE_DIR, "sky-out", "app");
const PORT = 8347; // chosen out of the way of dev servers

if (!existsSync(APP_BIN)) {
  console.error(
    `[probe] missing ${APP_BIN}; build 09-live-counter first (cd ${EXAMPLE_DIR} && sky build src/Main.sky)`,
  );
  process.exit(2);
}

console.log("[probe] starting one-off Sky.Live boot for liveJS extraction…");
const app = spawn(APP_BIN, [], {
  env: { ...process.env, SKY_LIVE_PORT: String(PORT) },
  cwd: EXAMPLE_DIR,
  stdio: ["ignore", "pipe", "pipe"],
});
app.stderr.on("data", (chunk) => process.stderr.write(`[app stderr] ${chunk}`));

// Wait for boot. The Sky.Live banner logs to stdout on listen.
await new Promise((res, rej) => {
  const t = setTimeout(() => rej(new Error("Sky.Live boot timeout")), 5000);
  app.stdout.on("data", (chunk) => {
    process.stdout.write(`[app] ${chunk}`);
    if (chunk.toString().includes("listening")) {
      clearTimeout(t);
      res();
    }
  });
});

// Pull the page HTML; grep out the runtime script block.
const html = await fetch(`http://127.0.0.1:${PORT}/`).then((r) => r.text());

// The runtime JS is the same-origin file /_sky/live.<hash>.js the page
// loads (live_client_asset.go; strict-CSP safe). Fetch it by the page's src.
const m = html.match(/<script src="([^"]*\/_sky\/live\.[0-9a-f]+\.js)"><\/script>/);
if (!m) {
  console.error("[probe] the page does not load /_sky/live.<hash>.js — runtime missing?");
  app.kill();
  process.exit(2);
}
const liveJS = await fetch(`http://127.0.0.1:${PORT}${m[1]}`).then((r) => r.text());

// Kill the app — we have what we need.
app.kill();

// ─── 2. Drive a blank Chromium page and inject liveJS ──────────

const browser = await chromium.launch({ headless: true });
const ctx = await browser.newContext();
const page = await ctx.newPage();
page.on("pageerror", (err) => console.warn("[browser pageerror]", err.message));
page.on("console", (msg) => {
  if (msg.type() === "error") console.warn("[browser console]", msg.text());
});

// A blank page with one container under sky-root. The probe then
// mounts attacker HTML into it, runs __skyReviveScripts, and reads
// back the resulting DOM + a window-scoped sentinel.
//
// setContent uses document.write under the hood which mangles
// </script> sequences inside our injected JS. Instead, set a
// minimal page then injecting the runtime via addScriptTag —
// Playwright's addScriptTag escapes script-content correctly.
await page.setContent(
  `<!doctype html><html><head><meta charset="utf-8"></head><body>
<div id="sky-root"></div>
</body></html>`,
  { waitUntil: "domcontentloaded" },
);
await page.addScriptTag({ content: liveJS });

const failures = [];

// Probe 1: <script onerror="...alert..."> with a fetch-failing src.
// The onerror SHOULD be stripped; the load failure SHOULD NOT fire a
// callback. Sentinel: window.__pwnedOnerror.
{
  const result = await page.evaluate(() => {
    window.__pwnedOnerror = false;
    const root = document.getElementById("sky-root");
    root.innerHTML =
      `<script src="/_nonexistent_xss_probe" onerror="window.__pwnedOnerror = true">` +
      `</script>`;
    // eslint-disable-next-line no-undef
    __skyReviveScripts(root);
    // Give the browser one tick to attempt the load + fire onerror.
    return new Promise((res) => {
      setTimeout(() => {
        const fresh = root.querySelector("script");
        res({
          pwned: !!window.__pwnedOnerror,
          hasOnerrorAttr: fresh ? fresh.hasAttribute("onerror") : null,
          hasSrcAttr: fresh ? fresh.hasAttribute("src") : null,
          markerSet: fresh
            ? fresh.getAttribute("data-sky-script-revived") === "1"
            : false,
        });
      }, 200);
    });
  });
  if (result.pwned) failures.push(`PROBE 1: onerror callback fired (XSS landed)`);
  if (result.hasOnerrorAttr) failures.push(`PROBE 1: onerror attr survived revival`);
  if (!result.hasSrcAttr) failures.push(`PROBE 1: legitimate src= dropped — regression`);
  if (!result.markerSet) failures.push(`PROBE 1: revival marker not set`);
  console.log(`[probe 1: onerror+src] ${JSON.stringify(result)}`);
}

// Probe 2: inline <script>window.__pwned = true</script> with NO src.
// Inline-without-src must be DROPPED entirely; the body must NOT
// execute; the source element gets the revival marker so a follow-up
// pass doesn't re-warn.
{
  const result = await page.evaluate(() => {
    window.__pwnedInline = false;
    const root = document.getElementById("sky-root");
    root.innerHTML =
      `<script>window.__pwnedInline = true;</script>`;
    // eslint-disable-next-line no-undef
    __skyReviveScripts(root);
    // The original <script> stays in place (the rejection skips
    // replaceChild) but its body never executes because it was
    // inserted via innerHTML.
    return {
      pwned: !!window.__pwnedInline,
      sourceMarker: root.querySelector("script")?.getAttribute("data-sky-script-revived") === "1",
    };
  });
  if (result.pwned) failures.push(`PROBE 2: inline-without-src body executed (XSS landed)`);
  if (!result.sourceMarker) failures.push(`PROBE 2: revival marker not set on rejected element`);
  console.log(`[probe 2: inline-no-src] ${JSON.stringify(result)}`);
}

// Probe 3: <script onclick="..." onmouseover="..." data-foo="bar" src="…">.
// All three non-allowlisted attrs MUST be dropped on revival; the
// src= survives (it's allowlisted).
{
  const result = await page.evaluate(() => {
    const root = document.getElementById("sky-root");
    root.innerHTML =
      `<script onclick="window.__pwnedClick=true" ` +
      `onmouseover="window.__pwnedHover=true" ` +
      `data-foo="bar" ` +
      `src="/_nonexistent_xss_probe">` +
      `</script>`;
    // eslint-disable-next-line no-undef
    __skyReviveScripts(root);
    const fresh = root.querySelector("script");
    return {
      hasOnclick: fresh?.hasAttribute("onclick") ?? null,
      hasOnmouseover: fresh?.hasAttribute("onmouseover") ?? null,
      hasDataFoo: fresh?.hasAttribute("data-foo") ?? null,
      hasSrc: fresh?.hasAttribute("src") ?? null,
    };
  });
  if (result.hasOnclick) failures.push(`PROBE 3: onclick attr survived revival`);
  if (result.hasOnmouseover) failures.push(`PROBE 3: onmouseover attr survived revival`);
  if (result.hasDataFoo) failures.push(`PROBE 3: non-allowlisted data-foo attr survived`);
  if (!result.hasSrc) failures.push(`PROBE 3: legitimate src= dropped — regression`);
  console.log(`[probe 3: multi-attr] ${JSON.stringify(result)}`);
}

// Probe 4: legitimate Sky-bundled-style <script src="…" type="module"
// async defer integrity="…" crossorigin="anonymous">. ALL these attrs
// are allowlisted and MUST survive revival.
{
  const result = await page.evaluate(() => {
    const root = document.getElementById("sky-root");
    root.innerHTML =
      `<script ` +
      `src="/sky-editor-bundle.js" ` +
      `type="module" ` +
      `async ` +
      `defer ` +
      `integrity="sha256-abc" ` +
      `crossorigin="anonymous" ` +
      `referrerpolicy="no-referrer">` +
      `</script>`;
    // eslint-disable-next-line no-undef
    __skyReviveScripts(root);
    const fresh = root.querySelector("script");
    return {
      hasSrc: fresh?.hasAttribute("src"),
      hasType: fresh?.hasAttribute("type"),
      hasAsync: fresh?.hasAttribute("async"),
      hasDefer: fresh?.hasAttribute("defer"),
      hasIntegrity: fresh?.hasAttribute("integrity"),
      hasCrossorigin: fresh?.hasAttribute("crossorigin"),
      hasReferrerpolicy: fresh?.hasAttribute("referrerpolicy"),
    };
  });
  for (const [k, v] of Object.entries(result)) {
    if (!v) failures.push(`PROBE 4: allowlisted attr ${k} dropped — regression`);
  }
  console.log(`[probe 4: allowlisted attrs] ${JSON.stringify(result)}`);
}

await browser.close();

if (failures.length === 0) {
  console.log("[probe] PASS — XSS hardening intact across 4 probe shapes");
  process.exit(0);
} else {
  console.error("[probe] FAIL — one or more revival regressions:");
  for (const f of failures) console.error("  - " + f);
  process.exit(1);
}
