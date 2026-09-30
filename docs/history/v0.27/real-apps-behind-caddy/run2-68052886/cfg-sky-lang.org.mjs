const MODE = process.env.CONSOLE_MODE || 'app';
const TOKEN = process.env.SKY_CONSOLE_TOKEN || '';

async function devLogin({ page, BASE, hydrated }) {
  const resp = await page.goto(BASE + '/admin/dev-login', { waitUntil: 'load' });
  const booted = await hydrated(page);
  await page.waitForTimeout(1000);
  return { landedOn: new URL(page.url()).pathname, status: resp?.status() ?? null, booted, signedInText: await page.getByText('Signed in as localadmin').count() };
}

export default {
  name: 'sky-lang.org',
  base: 'https://sky-lang.localhost:9443',
  evilOrigin: 'https://evil.localhost:9443',
  pages: ['/', '/blog', '/blog/why-i-built-sky-lang', '/admin'],
  rpcGuardPath: '/_rpc/LoadPosts',
  rpcGuardBody: JSON.stringify({ page: ['BlogIndex'] }),
  consoleLogin: async ({ ctx, page, BASE, cookieView }) => {
    if (MODE === 'token') {
      const bad = await ctx.request.post(BASE + '/_sky/console/_login', { form: { token: 'wrong' } });
      const resp = await ctx.request.post(BASE + '/_sky/console/_login', { form: { token: TOKEN }, maxRedirects: 0 });
      const after = await page.goto(BASE + '/_sky/console/', { waitUntil: 'load' });
      await page.waitForTimeout(2000);
      return { mode: 'token', wrongTokenStatus: bad.status(), loginStatus: resp.status(), loginLocation: resp.headers()['location'] || null, consoleStatusAfterLogin: after?.status() ?? null, consoleTitle: await page.title(), cookies: cookieView(await ctx.cookies(BASE)) };
    }
    // app mode: the site's own admin sign-in is the credential.
    const signIn = await devLogin({ page, BASE, hydrated: async () => true });
    // Not followed in the browser: the fallback redirect goes to the GitHub OAuth page.
    const link = await ctx.request.get(BASE + '/admin/console-link', { maxRedirects: 0 });
    const direct = await page.goto(BASE + '/_sky/console/', { waitUntil: 'load' });
    return { mode: 'app', signIn, consoleLinkStatus: link.status(), consoleLinkLocation: link.headers()['location'] || null, consoleStatusAfterAdminSignIn: direct?.status() ?? null, cookies: cookieView(await ctx.cookies(BASE)) };
  },
  scenarios: {
    adminSignInAndRpc: async ({ ctx, page, BASE, hydrated, cookieView, browser }) => {
      await page.goto(BASE + '/', { waitUntil: 'load' });
      await hydrated(page);
      const before = cookieView(await ctx.cookies(BASE));
      const signIn = await devLogin({ page, BASE, hydrated });
      const after = cookieView(await ctx.cookies(BASE));
      await page.getByText('+ New post').click();
      await page.getByPlaceholder('Post title').fill('Caddy verify draft ' + Date.now());
      await page.getByPlaceholder('url-slug').fill('caddy-verify-' + Date.now());
      const [rpcResp] = await Promise.all([
        page.waitForResponse((r) => r.url().includes('/_rpc/EditorSaveDraft'), { timeout: 20000 }),
        page.getByText('Save draft').click(),
      ]);
      await page.waitForTimeout(1500);
      const bodyText = await page.locator('body').innerText();
      // Sign out, then replay the admin cookie from before sign-out.
      const old = (await ctx.cookies(BASE)).find((c) => c.name === 'skylang_admin');
      const probe = async (val) => {
        const c2 = await browser.newContext({ ignoreHTTPSErrors: true });
        await c2.addCookies([{ name: 'skylang_admin', value: val, url: BASE, secure: true, httpOnly: true, sameSite: 'Lax' }]);
        const con = await c2.request.get(BASE + '/_sky/console/');
        const html = await (await c2.request.get(BASE + '/admin')).text();
        const r = { consoleStatus: con.status(), adminPageSignedIn: html.includes('localadmin') };
        await c2.close();
        return r;
      };
      const controlBeforeLogout = old ? await probe(old.value) : null;
      const lo = await ctx.request.get(BASE + '/admin/logout', { maxRedirects: 0 });
      const afterLogout = cookieView(await ctx.cookies(BASE));
      let replay = null;
      if (old) replay = await probe(old.value);
      return {
        adminCookie: old ? { name: old.name, secure: old.secure, httpOnly: old.httpOnly, sameSite: old.sameSite } : null,
        controlSameCookieBeforeLogout: controlBeforeLogout, logoutStatus: lo.status(), cookiesAfterLogout: afterLogout, replayOfCookieFromBeforeLogout: replay, cookiesBefore: before, signIn, cookiesAfter: after, saveDraftRpc: { status: rpcResp.status(), contentType: rpcResp.headers()['content-type'] || null }, flashShown: /Draft saved|Saved|saved/i.test(bodyText) };
    },
  },
};
