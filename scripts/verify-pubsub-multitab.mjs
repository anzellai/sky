#!/usr/bin/env node
// Cycle 3 P49 — Playwright multi-tab pub/sub probe.
//
// Driven by scripts/verify-pubsub-multitab.sh (which spawns the app
// and tears it down). This script:
//
//   1. Opens two browser CONTEXTS (each gets its own sky_sid cookie,
//      so the runtime treats them as independent sessions sharing a
//      topic).
//   2. Both navigate to /chat/lounge — the URL routing wires
//      `model.page = RoomPage "lounge"` and `subscriptions` opens a
//      subscribeTopic on "chat:room-lounge" for each session.
//   3. Tab A types + submits; we time-stamp the moment the submit
//      fires AND the moment Tab B's chat-log gains the message.
//   4. Tab B types + submits; same timing in the other direction.
//   5. Echo-to-publisher: assert tab A also sees its own message
//      after submitting (matches the design-doc Q2 default —
//      §11.2.5 Cross-process broker tiers, echo-by-default).
//
// Assertions:
//   * Each direction's latency < 500 ms (intra-process design SLA).
//   * Echo to publisher present.
//   * Tab A does NOT see "before" messages typed before joined the
//     room (sanity — no cross-room bleed).
//
// Exits 0 with `PASS verify-pubsub-multitab` + latencies on stdout
// when all assertions hold; exits 1 with `FAIL …` otherwise.

import { chromium } from 'playwright';

const PORT = parseInt(process.env.PUBSUB_PORT || '8127', 10);
const BASE_URL = `http://localhost:${PORT}`;
const ROOM = 'lounge';
const TIMEOUT_MS = 5000;        // generous; the design SLA is 500ms
const SLA_MS = 500;             // hard fail above this
const SETTLE_MS = 250;

async function waitForChatLog(page, expectText, since, deadline) {
    while (Date.now() < deadline) {
        const content = await page.locator('#chat-log').innerText().catch(() => '');
        if (content.includes(expectText)) {
            return Date.now() - since;
        }
        await page.waitForTimeout(20);
    }
    return null;
}

async function sendMessage(page, text) {
    const input = page.locator('#chat-input');
    await input.fill(text);
    await input.press('Enter');
}

async function joinRoom(page, name) {
    // Navigate to /chat/<room> directly so the URL routing places
    // model.page = RoomPage "lounge" without going through the
    // lobby form. The lobby form is wired for users who land at /;
    // it's not on the critical path for the pub/sub probe.
    await page.goto(`${BASE_URL}/chat/${ROOM}`, { waitUntil: 'domcontentloaded' });
    // Give the SSE handshake a moment to land (the runtime sends
    // event:hello then heartbeats; we want to be sure the
    // subscription goroutine is parked before we publish).
    await page.waitForTimeout(SETTLE_MS);
}

function fail(reason) {
    console.error('FAIL verify-pubsub-multitab');
    console.error('    ' + reason);
    process.exit(1);
}

async function main() {
    const browser = await chromium.launch({ headless: true });

    let tabA, tabB, ctxA, ctxB;
    try {
        ctxA = await browser.newContext();
        ctxB = await browser.newContext();
        tabA = await ctxA.newPage();
        tabB = await ctxB.newPage();

        // Surface server-side errors as test failures.
        const consoleErrors = [];
        for (const [name, p] of [['A', tabA], ['B', tabB]]) {
            p.on('console', msg => {
                if (msg.type() === 'error') {
                    consoleErrors.push(`tab${name}: ${msg.text()}`);
                }
            });
            p.on('pageerror', err => {
                consoleErrors.push(`tab${name}: ${err.message}`);
            });
        }

        await Promise.all([joinRoom(tabA, 'alice'), joinRoom(tabB, 'bob')]);

        // ----- A → B ---------------------------------------------
        const tagAB = `from-A-${Date.now()}`;
        const startAB = Date.now();
        await sendMessage(tabA, tagAB);
        const latencyAB = await waitForChatLog(tabB, tagAB, startAB,
            startAB + TIMEOUT_MS);
        if (latencyAB === null) {
            fail(`tab B did not receive "${tagAB}" within ${TIMEOUT_MS}ms`);
        }
        if (latencyAB > SLA_MS) {
            fail(`A→B latency ${latencyAB}ms exceeds SLA ${SLA_MS}ms`);
        }

        // ----- Echo to publisher (A sees its own message) --------
        // The locked default (design doc Q2) — publisher's own
        // subscription receives the broadcast too. App can self-skip
        // on Origin == sess.sid if not desired.
        const latencyEcho = await waitForChatLog(tabA, tagAB, startAB,
            startAB + TIMEOUT_MS);
        if (latencyEcho === null) {
            fail(`tab A (publisher) did not echo "${tagAB}" within ${TIMEOUT_MS}ms`);
        }
        if (latencyEcho > SLA_MS) {
            fail(`echo latency ${latencyEcho}ms exceeds SLA ${SLA_MS}ms`);
        }

        // ----- B → A ---------------------------------------------
        const tagBA = `from-B-${Date.now()}`;
        const startBA = Date.now();
        await sendMessage(tabB, tagBA);
        const latencyBA = await waitForChatLog(tabA, tagBA, startBA,
            startBA + TIMEOUT_MS);
        if (latencyBA === null) {
            fail(`tab A did not receive "${tagBA}" within ${TIMEOUT_MS}ms`);
        }
        if (latencyBA > SLA_MS) {
            fail(`B→A latency ${latencyBA}ms exceeds SLA ${SLA_MS}ms`);
        }

        // ----- Sanity — no cross-room bleed ----------------------
        // Open a third context on a DIFFERENT room and assert it does
        // NOT see any of the messages above.
        const ctxC = await browser.newContext();
        const tabC = await ctxC.newPage();
        await tabC.goto(`${BASE_URL}/chat/other-room`, { waitUntil: 'domcontentloaded' });
        await tabC.waitForTimeout(SETTLE_MS);
        const cContent = await tabC.locator('#chat-log').innerText().catch(() => '');
        if (cContent.includes(tagAB) || cContent.includes(tagBA)) {
            fail(`cross-room bleed detected: room "other-room" saw "${tagAB}" or "${tagBA}"`);
        }
        await ctxC.close();

        if (consoleErrors.length > 0) {
            fail('console errors: ' + consoleErrors.slice(0, 3).join(' | '));
        }

        console.log('PASS verify-pubsub-multitab');
        console.log(`    A→B  latency_ms=${latencyAB}`);
        console.log(`    B→A  latency_ms=${latencyBA}`);
        console.log(`    echo latency_ms=${latencyEcho}`);
        process.exit(0);
    } catch (err) {
        fail(`exception: ${err.message}\n${err.stack}`);
    } finally {
        if (ctxA) await ctxA.close().catch(() => {});
        if (ctxB) await ctxB.close().catch(() => {});
        await browser.close().catch(() => {});
    }
}

main().catch(err => {
    console.error('FAIL verify-pubsub-multitab — driver error: ' + err.message);
    console.error(err.stack);
    process.exit(1);
});
