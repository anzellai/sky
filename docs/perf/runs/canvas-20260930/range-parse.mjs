#!/usr/bin/env node
// docs/perf/runs/canvas-20260930/range-parse.mjs
//
// The cost of the three ways to parse one new SVG child into an <svg>, N
// times, each in a fresh page, headless: a Range per child (the Sky.Live and
// desktop clients before this run), one Range reused, and a detached <svg>
// whose innerHTML is set (the clients after). Output: range-parse.txt.
import pw from "playwright";
for (const b of ["webkit", "chromium"]) {
  const br = await pw[b].launch({ headless: true });
  const out = {};
  for (const mode of ["range-per-kid", "one-range", "svg-innerHTML"]) for (const n of [1000, 5000]) {
    const page = await br.newPage();
    await page.setContent('<svg id="s" xmlns="http://www.w3.org/2000/svg"></svg>');
    out[mode + " " + n] = await page.evaluate(([mode, n]) => {
      const s = document.getElementById("s"); let shared = null; const t0 = performance.now();
      for (let i = 0; i < n; i++) {
        const html = '<circle cx="' + i + '" cy="5" r="2"/>';
        if (mode === "svg-innerHTML") { const t = document.createElementNS("http://www.w3.org/2000/svg", "svg"); t.innerHTML = html; while (t.firstChild) s.appendChild(t.firstChild); continue; }
        let range = shared;
        if (!range) { range = document.createRange(); range.selectNodeContents(s); if (mode === "one-range") shared = range; }
        s.appendChild(range.createContextualFragment(html));
      }
      return Math.round(performance.now() - t0);
    }, [mode, n]);
    await page.close();
  }
  console.log(b, JSON.stringify(out));
  await br.close();
}
