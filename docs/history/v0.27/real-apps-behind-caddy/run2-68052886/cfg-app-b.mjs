import fs from 'fs';
const LOG = process.env.APP_LOG;
const BASE0 = 'https://app-b.localhost:9443';

async function verifyTokenFor(email) {
  for (let i = 0; i < 40; i++) {
    const txt = fs.readFileSync(LOG, 'utf8');
    const at = txt.lastIndexOf(email);
    if (at >= 0) {
      const m = txt.slice(at).match(/\/verify\/([A-Za-z0-9_-]{8,})/);
      if (m) return m[1];
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error('no verify link logged for ' + email);
}

const sid = async (ctx) => (await ctx.cookies(BASE0)).find((c) => c.name === 'sky_sid' || c.name === '__Host-sky_sid');

async function signUpAndVerify({ page, BASE, hydrated }, email, password) {
  await page.goto(BASE + '/signup', { waitUntil: 'load' });
  await hydrated(page);
  await page.locator('input[name="name"]').fill('Verify Bot');
  await page.locator('input[name="email"]').fill(email);
  await page.locator('input[name="password"]').fill(password);
  const [r] = await Promise.all([
    page.waitForResponse((x) => x.url().includes('/_rpc/DoSignUp'), { timeout: 20000 }),
    page.getByRole('button', { name: /create|sign up/i }).first().click(),
  ]);
  await page.waitForTimeout(1500);
  const afterSignUp = await page.locator('body').innerText();
  const signUpUi = { path: new URL(page.url()).pathname, noticeAccountCreated: /Account created!/.test(afterSignUp) };
  const token = await verifyTokenFor(email);
  const [v] = await Promise.all([
    page.waitForResponse((x) => x.url().includes('/_rpc/RunVerify') || x.url().includes('/_rpc/ForceVerify'), { timeout: 25000 }).catch(() => null),
    page.goto(BASE + '/verify/' + token, { waitUntil: 'load' }),
  ]);
  await hydrated(page);
  await page.waitForTimeout(2000);
  const afterVerify = await page.locator('body').innerText();
  return { signUpRpc: r.status(), signUpUi, verifyRpc: v ? { path: new URL(v.url()).pathname, status: v.status() } : null, verifyUi: { path: new URL(page.url()).pathname, noticeVerified: /Email verified/.test(afterVerify) } };
}

async function ssrSignedIn(browser, BASE, cookieValue, email) {
  // A brand-new context that carries ONLY the given sky_sid value.
  const c = await browser.newContext({ ignoreHTTPSErrors: true });
  if (cookieValue) await c.addCookies([{ name: 'sky_sid', value: cookieValue, url: BASE, secure: true, httpOnly: true, sameSite: 'Lax' }]);
  const r = await c.request.get(BASE + '/account');
  const html = await r.text();
  await c.close();
  return { status: r.status(), seedCarriesEmail: html.includes(email) };
}

export default {
  name: 'app-b',
  base: BASE0,
  evilOrigin: 'https://evil.localhost:9443',
  pages: ['/', '/about', '/products', '/products/sample-product', '/basket', '/signin', '/signup', '/suggest', '/terms', '/privacy'],
  rpcGuardPath: '/_rpc/AddToBasket',
  rpcGuardBody: '{}',
  consoleLogin: async ({ ctx, page, BASE, cookieView }) => {
    const hydrated = async (p) => { try { await p.waitForFunction(() => !document.documentElement.hasAttribute('data-sky-hydrating'), null, { timeout: 30000 }); return true; } catch { return false; } };
    const email = `admin+${Date.now()}@local.test`.replace('admin+', 'admin@local.test'.split('@')[0] + '+');
    // APP_B_ADMIN_EMAILS is admin@local.test: sign up with exactly that address once per run.
    const adminEmail = process.env.ADMIN_EMAIL || 'admin@local.test';
    const r = await signUpAndVerify({ page, BASE, hydrated }, adminEmail, 'correct-horse-9');
    const after = await page.goto(BASE + '/_sky/console/', { waitUntil: 'load' });
    await page.waitForTimeout(2000);
    return { mode: 'app', adminSignUp: r, consoleStatusAfterAdminSignIn: after?.status() ?? null, consoleTitle: await page.title(), cookies: cookieView(await ctx.cookies(BASE)), note: email ? undefined : undefined };
  },
  scenarios: {
    basketRpc: async ({ ctx, page, BASE, hydrated }) => {
      await page.goto(BASE + '/products/sample-product', { waitUntil: 'load' });
      const booted = await hydrated(page);
      const [r] = await Promise.all([
        page.waitForResponse((x) => x.url().includes('/_rpc/AddToBasket'), { timeout: 20000 }),
        page.getByText('Add to basket').first().click(),
      ]);
      await page.waitForTimeout(1500);
      const inPage = await page.locator('body').innerText();
      await page.goto(BASE + '/basket', { waitUntil: 'load' });
      await hydrated(page);
      await page.waitForTimeout(1000);
      const basketText = await page.locator('body').innerText();
      return { booted, addToBasketRpc: r.status(), noticeShownInPage: /added to your basket/i.test(inPage), basketBadge: (inPage.match(/Basket\s*\d+/) || [null])[0], basketPageShowsItem: /Sample Product/.test(basketText), basketPageSaysEmpty: /Your basket is empty/.test(basketText) };
    },
    suggest: async ({ ctx, page, BASE, hydrated }) => {
      await page.goto(BASE + '/suggest', { waitUntil: 'load' });
      const booted = await hydrated(page);
      await page.locator('input[name="email"]').first().fill('idea@local.test');
      await page.locator('[name="body"]').first().fill('A double-decker bookmark, please.');
      const [r] = await Promise.all([
        page.waitForResponse((x) => x.url().includes('/_rpc/DoSuggest'), { timeout: 20000 }),
        page.getByRole('button', { name: /send my idea/i }).first().click(),
      ]);
      await page.waitForTimeout(1500);
      const t = await page.locator('body').innerText();
      return { booted, doSuggestRpc: r.status(), noticeThankYou: /Thank you/.test(t) };
    },
    signInRotation: async ({ ctx, page, BASE, hydrated, cookieView, browser }) => {
      const email = `buyer${Date.now()}@local.test`;
      const password = 'correct-horse-9';
      await page.goto(BASE + '/', { waitUntil: 'load' });
      await hydrated(page);
      const v0 = await sid(ctx);
      const su = await signUpAndVerify({ page, BASE, hydrated }, email, password);
      const v1 = await sid(ctx);
      // Sign out in the page.
      await page.goto(BASE + '/account', { waitUntil: 'load' });
      await hydrated(page);
      await page.waitForTimeout(1500);
      const [so] = await Promise.all([
        page.waitForResponse((x) => x.url().includes('/_rpc/__spaSignOut'), { timeout: 20000 }).catch(() => null),
        page.getByText('Sign out').first().click(),
      ]);
      await page.waitForTimeout(1500);
      const vOut = await sid(ctx);
      // Sign in with the password.
      await page.goto(BASE + '/signin', { waitUntil: 'load' });
      await hydrated(page);
      await page.locator('input[name="email"]').fill(email);
      await page.locator('input[name="password"]').fill(password);
      const [si] = await Promise.all([
        page.waitForResponse((x) => x.url().includes('/_rpc/DoSignIn'), { timeout: 20000 }),
        page.getByRole('button', { name: /sign in/i }).first().click(),
      ]);
      await page.waitForTimeout(2000);
      const afterSignIn = await page.locator('body').innerText();
      const signInUi = { path: new URL(page.url()).pathname, noticeWelcomeBack: /Welcome back/.test(afterSignIn), showsSignOut: /Sign out/i.test(afterSignIn) };
      const v2 = await sid(ctx);
      const view = (c) => (c ? { name: c.name, secure: c.secure, httpOnly: c.httpOnly, sameSite: c.sameSite, valuePrefix: c.value.slice(0, 12) } : null);
      return {
        signUp: su,
        cookieBeforeSignIn: view(v0),
        cookieAfterVerify: view(v1),
        signOutRpc: so ? so.status() : null,
        cookieAfterSignOut: view(vOut),
        signInRpc: si.status(),
        signInUi,
        cookieAfterPasswordSignIn: view(v2),
        valueChangedAtSignIn: !!(v1 && v2 && v1.value !== v2.value),
        valueChangedAnonToSignedIn: !!(v1 && (!v0 || v0.value !== v1.value)),
        replay_preSignInValue: v0 ? await ssrSignedIn(browser, BASE, v0.value, email) : 'no anonymous sky_sid was issued',
        replay_currentValue: v2 ? await ssrSignedIn(browser, BASE, v2.value, email) : null,
        replay_valueFromBeforeSignOut: v1 ? await ssrSignedIn(browser, BASE, v1.value, email) : null,
        control_noCookie: await ssrSignedIn(browser, BASE, null, email),
      };
    },
  },
};
