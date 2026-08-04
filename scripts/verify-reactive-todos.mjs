#!/usr/bin/env node
// Two-session reactive proof for examples/56-reactive-todos (P-R4a / P-R6).
//
// Two independent browser CONTEXTS (each its own sky_sid → independent sessions
// sharing the "todos" collection). Tab A adds a todo; we assert Tab B's list
// shows it WITHOUT a reload — driven purely by the BlueDB change-feed →
// broker → watchCollection → re-query → SSE patch. Also asserts the writer
// (Tab A) sees its own todo (self-notification via the change-feed).

import { chromium } from 'playwright';

const PORT = parseInt(process.env.REACTIVE_PORT || '8129', 10);
const BASE = `http://localhost:${PORT}`;
const SLA_MS = 1500; // reactive round-trip (change-feed → re-query → SSE)
const TIMEOUT_MS = 8000;
const SETTLE_MS = 700; // let the SSE handshake + subscription park

async function addTodo(page, text) {
    const input = page.locator('input').first();
    // Fill, confirm the value stuck (a concurrent re-render can clobber it), then
    // click Add. Retry the fill if a patch reset it mid-type.
    for (let i = 0; i < 5; i++) {
        await input.fill(text);
        await page.waitForTimeout(30);
        if ((await input.inputValue().catch(() => '')) === text) break;
    }
    await page.getByRole('button', { name: 'Add' }).click();
}

async function waitForText(page, text, since, deadline) {
    while (Date.now() < deadline) {
        const body = await page.locator('body').innerText().catch(() => '');
        if (body.includes(text)) return Date.now() - since;
        await page.waitForTimeout(20);
    }
    return null;
}

let code = 1;
async function main() {
    const browser = await chromium.launch({ headless: true });
    try {
        const ctxA = await browser.newContext();
        const ctxB = await browser.newContext();
        const tabA = await ctxA.newPage();
        const tabB = await ctxB.newPage();
        await tabA.goto(BASE, { waitUntil: 'domcontentloaded' });
        await tabB.goto(BASE, { waitUntil: 'domcontentloaded' });
        await tabA.waitForTimeout(SETTLE_MS);
        await tabB.waitForTimeout(SETTLE_MS);

        // Warmup handshake: pub/sub has no replay, so a write before a session's
        // watchCollection subscription is parked is missed. Add a warmup todo and
        // wait until BOTH tabs show it — that proves both subscriptions are live
        // before we measure. (A freshly loaded page's own init-query already has
        // current state; only this sub-second establishment window is racy.)
        const warm = 'warmup-' + Date.now();
        for (let attempt = 0; attempt < 10; attempt++) {
            await addTodo(tabA, warm);
            const wa = await waitForText(tabA, warm, Date.now(), Date.now() + 1500);
            const wb = await waitForText(tabB, warm, Date.now(), Date.now() + 1500);
            if (wa !== null && wb !== null) break;
            await tabA.waitForTimeout(300);
            if (attempt === 9) {
                console.error('FAIL verify-reactive-todos');
                console.error('    subscriptions never established (warmup never reached both tabs)');
                return;
            }
        }

        const text = 'reactive-' + Date.now();
        const t0 = Date.now();
        await addTodo(tabA, text);

        const latB = await waitForText(tabB, text, t0, t0 + TIMEOUT_MS);
        if (latB === null) {
            console.error('FAIL verify-reactive-todos');
            console.error('    tab B never saw the todo added by tab A (reactive push failed)');
            return;
        }
        const latA = await waitForText(tabA, text, t0, Date.now() + 3000);
        if (latA === null) {
            console.error('FAIL verify-reactive-todos');
            console.error('    tab A (writer) never saw its own todo (self-notification failed)');
            return;
        }

        // Second write, other direction, to rule out a one-shot fluke.
        const text2 = 'reactive2-' + Date.now();
        const t1 = Date.now();
        await addTodo(tabB, text2);
        const latA2 = await waitForText(tabA, text2, t1, t1 + TIMEOUT_MS);
        if (latA2 === null) {
            console.error('FAIL verify-reactive-todos');
            console.error('    tab A never saw the todo added by tab B (reverse direction failed)');
            return;
        }

        const warn = latB > SLA_MS || latA2 > SLA_MS ? ` (warn: >${SLA_MS}ms SLA)` : '';
        console.log(`PASS verify-reactive-todos  A→B=${latB}ms  B→A=${latA2}ms  self=${latA}ms${warn}`);
        code = 0;
    } catch (e) {
        console.error('FAIL verify-reactive-todos');
        console.error('    ' + (e && e.message ? e.message : String(e)));
    } finally {
        await browser.close();
    }
}

main().then(() => process.exit(code));
