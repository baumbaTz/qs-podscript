// README screenshots (docs/screenshots/*.png) from the demo data.
// 1. Demo data + placeholder tools, so Setup counts as done (see HANDOFF.md):
//      QSPS_DEMO_HOME=/tmp/shots go test -count=1 -tags sqlite_fts5 -run TestMakeDemo .
//      (plus empty files for whisper-cli, the whisper model and the speaker models)
// 2. QSPODSCRIPT_HOME=/tmp/shots ./qs-podscript serve --port 8398 --no-browser
// 3. NODE_PATH=$(npm root -g) node docs/dev/screenshots.js [out-dir]
// The look is switched to Classic for one picture and back to QuickSack.
const { chromium } = require('playwright');
const base = process.env.BASE || 'http://127.0.0.1:8398';
const out = process.argv[2] || 'docs/screenshots';
require('fs').mkdirSync(out, { recursive: true });

async function setLook(look) {
  await fetch(base + '/settings/look', {
    method: 'POST', redirect: 'manual',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded', Origin: base },
    body: 'look=' + look,
  });
}

(async () => {
  const b = await chromium.launch();
  const page = async (opts = {}) => {
    const ctx = await b.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: 'dark', ...opts });
    return ctx.newPage();
  };
  const settle = (p) => p.evaluate(() => document.fonts.ready).then(() => p.waitForTimeout(400));
  const shot = async (p, name, opts = {}) => { await settle(p); await p.screenshot({ path: `${out}/${name}.png`, ...opts }); console.log(name); };

  await setLook('quicksack');
  let p = await page();
  await p.goto(base + '/'); await shot(p, 'podcasts');
  await p.goto(base + '/episodes/1'); await shot(p, 'episode');
  await p.goto(base + '/feeds/1'); await shot(p, 'podcast');
  await p.goto(base + '/search?q=thing'); await shot(p, 'search');
  await p.goto(base + '/feeds/1/quiz'); await shot(p, 'look-whos-talking');
  await p.goto(base + '/setup#speakers'); await p.evaluate(() => document.getElementById('speakers').scrollIntoView()); await shot(p, 'setup');

  // correcting: double-click a word in the transcript
  await p.goto(base + '/episodes/1'); await settle(p);
  const w = p.locator('#transcript .text span', { hasText: 'Bottin' }).first();
  await w.scrollIntoViewIfNeeded(); await p.evaluate(() => window.scrollBy(0, 250));
  await w.dblclick(); await p.waitForTimeout(500);
  await shot(p, 'correction');

  // bright mode
  p = await page({ colorScheme: 'light' });
  await p.goto(base + '/episodes/1'); await shot(p, 'episode-light');

  // phone
  p = await page({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 1, isMobile: true, hasTouch: true });
  await p.goto(base + '/episodes/1'); await shot(p, 'phone');

  // the other look
  await setLook('classic');
  p = await page();
  await p.goto(base + '/episodes/1'); await shot(p, 'episode-classic');
  await setLook('quicksack');

  await b.close();
})();
