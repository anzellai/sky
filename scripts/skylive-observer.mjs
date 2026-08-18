// One real browser, observing what a user perceives while synthetic
// load scales.
//
// WHY A BROWSER AT ALL, WHEN skyliveload ALREADY MEASURES LATENCY
//
// Two reasons, and the second is the important one.
//
// 1. It measures the right thing. "How many users can we serve without
//    it feeling slow" is a question about perceived latency in a real
//    client, not about a percentile in a server log. Chromium runs the
//    actual Sky.Live client JS -- the SSE reconnect logic, the seq
//    guard, sky-nav, and the DOM patch application -- so the number it
//    reports includes client-side patch cost, which a protocol-level
//    client cannot see at all.
//
// 2. It is the strongest available check that the Go generator is
//    faithful. A synthetic client that quietly diverges from the real
//    protocol produces confident, meaningless throughput. Running a
//    genuine browser against the same app and comparing what it
//    receives is a far better test than any assertion the generator
//    could make about itself.
//
// The browser is an OBSERVER, never load: one Chromium is 100-300 MB
// and this is a 16 GB machine. Load comes from skyliveload.
//
// Usage:
//   node scripts/skylive-observer.mjs --url http://127.0.0.1:8000 \
//        --samples 30 --json out.json

import { chromium } from 'playwright';

function arg(name, dflt) {
  const i = process.argv.indexOf(`--${name}`);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : dflt;
}

const URL_ = arg('url', 'http://127.0.0.1:8000');
const SAMPLES = parseInt(arg('samples', '30'), 10);
const THINK_MS = parseInt(arg('think', '250'), 10);
const JSON_OUT = arg('json', '');
const LABEL = arg('label', '');

function percentile(sorted, p) {
  if (!sorted.length) return 0;
  return sorted[Math.min(sorted.length - 1, Math.floor((sorted.length - 1) * p))];
}

const browser = await chromium.launch({
  headless: true,
  args: ['--no-sandbox', '--disable-dev-shm-usage'],
});

let result;
try {
  const page = await browser.newPage();
  const consoleErrors = [];
  page.on('pageerror', (e) => consoleErrors.push(String(e)));

  await page.goto(URL_, { waitUntil: 'domcontentloaded', timeout: 30000 });

  // The runtime binds handlers on DOMContentLoaded and opens the SSE
  // stream immediately (live.go:8912/8929). Wait for the client to
  // actually be live rather than sleeping a guessed interval.
  await page.waitForFunction(
    () => typeof window.__skySid !== 'undefined' && window.__skySid !== '',
    { timeout: 15000 },
  );

  // Find a clickable element the runtime has bound. `sky-click` is the
  // attribute renderVNode emits for a bound click handler (live.go:418).
  const clickable = await page.$('[sky-click]');
  if (!clickable) {
    throw new Error(
      'no [sky-click] element on the page: nothing for the observer to interact ' +
        'with, so any latency it reported would be fictional',
    );
  }

  // MEASUREMENT: time from the click to the DOM actually changing.
  //
  // A network timing would be easier but wrong for this question -- the
  // point is perceived latency, so the signal has to be a user-visible
  // one. A MutationObserver inside the page gives exactly that: the
  // moment the patch lands in the DOM.
  await page.evaluate(() => {
    window.__obsSamples = [];
    window.__obsPending = null;
    const obs = new MutationObserver(() => {
      if (window.__obsPending !== null) {
        window.__obsSamples.push(performance.now() - window.__obsPending);
        window.__obsPending = null;
      }
    });
    obs.observe(document.getElementById('sky-root') || document.body, {
      subtree: true,
      childList: true,
      characterData: true,
      attributes: true,
    });
  });

  let timeouts = 0;
  for (let i = 0; i < SAMPLES; i++) {
    await page.evaluate(() => {
      window.__obsPending = performance.now();
    });
    await clickable.click({ timeout: 5000 }).catch(() => {});
    try {
      await page.waitForFunction(() => window.__obsPending === null, { timeout: 5000 });
    } catch {
      timeouts++;
      await page.evaluate(() => {
        window.__obsPending = null;
      });
    }
    if (THINK_MS > 0) await page.waitForTimeout(THINK_MS);
  }

  const samples = await page.evaluate(() => window.__obsSamples);
  samples.sort((a, b) => a - b);

  result = {
    label: LABEL,
    url: URL_,
    samples_requested: SAMPLES,
    samples_observed: samples.length,
    timeouts,
    p50_ms: +percentile(samples, 0.5).toFixed(2),
    p95_ms: +percentile(samples, 0.95).toFixed(2),
    p99_ms: +percentile(samples, 0.99).toFixed(2),
    max_ms: +(samples[samples.length - 1] ?? 0).toFixed(2),
    page_errors: consoleErrors.slice(0, 5),
    // A run where the DOM never changed is not a fast run -- it is a
    // broken one. Surfaced rather than reported as excellent latency.
    valid: samples.length > 0 && samples.length >= SAMPLES * 0.5,
    invalid_reason:
      samples.length === 0
        ? 'the DOM never changed after a click: the observer measured nothing'
        : samples.length < SAMPLES * 0.5
          ? `only ${samples.length}/${SAMPLES} clicks produced a DOM change`
          : undefined,
  };

  console.log(JSON.stringify(result, null, 2));
} finally {
  await browser.close();
}

if (JSON_OUT) {
  const { writeFileSync } = await import('fs');
  writeFileSync(JSON_OUT, JSON.stringify(result, null, 2) + '\n');
}

if (!result?.valid) process.exit(2);
