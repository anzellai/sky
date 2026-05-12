import { chromium } from "playwright";
const browser = await chromium.launch({ headless: true });
const page = await browser.newPage();
const errors = [];
page.on("pageerror", (e) => { errors.push("PAGEERROR: " + e.message); });
page.on("response", (r) => { if (r.status() >= 500) errors.push(`HTTP ${r.status()} ${r.url()}`); });

console.log("=== unauthenticated visit / ===");
await page.goto("http://localhost:8000/", { waitUntil: "domcontentloaded" });
await page.waitForTimeout(500);
const body0 = await page.locator("body").innerText();
console.log("board has 'Test idea title':", body0.includes("Test idea title"));
console.log("body (first 400):", body0.substring(0, 400));

console.log("=== click the idea card (unauthenticated) ===");
// Locate by text — the idea title
const ideaCard = page.locator("text=Test idea title");
const cardCount = await ideaCard.count();
console.log("idea card count:", cardCount);
if (cardCount > 0) {
    await ideaCard.first().click();
    await page.waitForTimeout(1500);
    const after = await page.locator("body").innerText();
    console.log("post-click URL:", page.url());
    console.log("post-click body (first 500):", after.substring(0, 500));
}

console.log("=== final errors ===");
errors.forEach(e => console.log("  ", e));

await browser.close();
