// Phone-width check (412 px): opens the pages, reports any page that scrolls
// sideways and the element that causes it, and saves full-page screenshots.
// Needs a running app with the demo data (see HANDOFF.md, "Demo data"):
//   QSPODSCRIPT_HOME=/tmp/demo ./qs-podscript server --listen 127.0.0.1:8399
//   NODE_PATH=$(npm root -g) node docs/dev/phonecheck.js
// Options (environment):
//   BASE=http://127.0.0.1:8398   other address (e.g. local mode: "serve")
//   MODE=server|public|local     server = logged in as demo (default),
//                                public = not logged in, local = no login
//   OUT=/tmp/qsps-phone          where the screenshots go
//   LBL=0                        voice label for the voice page
const { chromium } = require('playwright');
const base = process.env.BASE || 'http://127.0.0.1:8399';
const mode = process.env.MODE || 'server';
const out = process.env.OUT || '/tmp/qsps-phone/' + mode;
require('fs').mkdirSync(out, { recursive: true });
const lbl = process.env.LBL || '0';
const editPages = ['/feeds/1/quiz', '/episodes/1/quiz', '/episodes/1/voice/' + lbl, '/people', '/people/1', '/setup', '/queue', '/log', '/help'];
const pages = {
  server: ['/', '/work', '/users', '/account', '/users/1', '/feeds/1', '/episodes/1', '/search?q=thing', ...editPages],
  public: ['/', '/feeds/1', '/episodes/1', '/search?q=thing', '/login', '/help'],
  local: ['/', '/feeds/1', '/episodes/1', '/search?q=thing', ...editPages],
}[mode];
(async () => {
  const b = await chromium.launch();
  const ctx = await b.newContext({ viewport: { width: 412, height: 900 }, deviceScaleFactor: 1, isMobile: true, hasTouch: true });
  const p = await ctx.newPage();
  if (mode === 'server') {
    await p.goto(base + '/login');
    await p.fill('input[name=name]', 'demo'); await p.fill('input[name=password]', 'demo-password');
    await Promise.all([p.waitForNavigation(), p.click('main button[type=submit]')]);
  }
  for (const path of pages) {
    const r = await p.goto(base + path);
    await p.waitForTimeout(300);
    const info = await p.evaluate(() => {
      const W = document.documentElement.clientWidth;
      const sw = document.documentElement.scrollWidth;
      const bad = [];
      for (const el of document.querySelectorAll('body *')) {
        const rc = el.getBoundingClientRect();
        if (rc.width > 0 && rc.right > W + 1) {
          // report only the outermost offenders; skip things inside a box
          // that scrolls on its own (overflow-x auto/hidden)
          let par = el.parentElement, skip = false;
          while (par && par !== document.body) {
            const pr = par.getBoundingClientRect();
            const ox = getComputedStyle(par).overflowX;
            if (pr.right > W + 1 || ox === 'auto' || ox === 'hidden' || ox === 'scroll') { skip = true; break; }
            par = par.parentElement;
          }
          if (!skip) bad.push(el.tagName.toLowerCase() + (el.className ? '.' + String(el.className).replace(/\s+/g, '.') : '') + ' right=' + Math.round(rc.right));
        }
      }
      // tap targets smaller than 32 px (buttons, links in menus)
      const small = [];
      for (const el of document.querySelectorAll('button, input[type=submit], select, .btn')) {
        const rc = el.getBoundingClientRect();
        if (rc.width > 0 && rc.height > 0 && rc.height < 28) small.push(el.tagName.toLowerCase() + ':' + (el.value || el.textContent || el.name || '').trim().replace(/\s+/g, ' ').slice(0, 20) + '=' + Math.round(rc.height));
      }
      return { sw, W, bad: bad.slice(0, 6), small: small.slice(0, 8) };
    });
    console.log(r.status(), path, info.sw > info.W ? 'OVERFLOW ' + info.sw + '>' + info.W : 'ok', info.bad.join(' | '), info.small.length ? ' small: ' + info.small.join(', ') : '');
    const name = path.replace(/[^a-z0-9]+/gi, '_') || 'home';
    await p.screenshot({ path: `${out}/${name}.png`, fullPage: true });
  }
  // scrolled (compact) header
  await p.goto(base + '/'); await p.evaluate(() => window.scrollTo(0, 500)); await p.waitForTimeout(500);
  await p.screenshot({ path: `${out}/home_scrolled.png` });
  await b.close();
})();
