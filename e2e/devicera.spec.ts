import { test as base, expect, type Page } from '@playwright/test';
import fs from 'fs';
import path from 'path';

// Recorded once from a live `mdi serve` for "pacemaker" and kept verbatim.
const FIXTURE_DIR = path.join(__dirname, 'fixtures', 'pacemaker');
const DEVICE = 'pacemaker';
const API_ENDPOINTS = ['dossier', 'signals', 'trend', 'failure-modes', 'devices'];

const XLSX_URL = 'https://cdn.jsdelivr.net/npm/xlsx-js-style@1.2.0/dist/xlsx.bundle.js';
const XLSX_LOCAL = require.resolve('xlsx-js-style/dist/xlsx.bundle.js');

// The signal types the attention index averages. Mirrors signalDirections in
// internal/intelligence/12-synthesis.go, kept here on purpose rather than read
// from the page, so a page that miscounts cannot also redefine the answer.
const ACTIVITY_TYPES = ['VOLUME', 'VOLUME_SHIFT', 'CORROBORATION', 'LIFECYCLE_PHASE', 'RECALL_RECENCY'];
const ACTIVITY_LABELS = ['Quiet', 'Moderate', 'Busy', 'Top'];

// The trend and problems cards share the .signal-card class (and .sig-code);
// only real signal cards carry a value.
const SIGNAL_CARD = '.signal-card:has(.sig-value)';

// The Device Records main table (strong matches) and the collapsed section
// holding the weak ones (matched only through a secondary FDA product code).
const MAIN_ROWS = '.dev-card > .dev-table-wrap .dev-table tbody tr';
const WEAK_SECTION = '.dev-card details.weak-matches';

function fixture(name: string): any {
  return JSON.parse(fs.readFileSync(path.join(FIXTURE_DIR, name + '.json'), 'utf8'));
}

const strongRecords = (d: any): any[] => d.records.filter((r: any) => r.match_strength === 'strong');
const weakRecords = (d: any): any[] => d.records.filter((r: any) => r.match_strength === 'weak');

// RFC 4180 reader for the CSV export (quoted cells, doubled quotes, CRLF).
function parseCSV(text: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [], cell = '', quoted = false;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (quoted) {
      if (ch === '"' && text[i + 1] === '"') { cell += '"'; i++; }
      else if (ch === '"') quoted = false;
      else cell += ch;
    } else if (ch === '"') quoted = true;
    else if (ch === ',') { row.push(cell); cell = ''; }
    else if (ch === '\n') { row.push(cell.replace(/\r$/, '')); rows.push(row); row = []; cell = ''; }
    else cell += ch;
  }
  if (cell !== '' || row.length) { row.push(cell); rows.push(row); }
  return rows;
}

type Harness = {
  // Uncaught page exceptions (window 'error' / unhandled rejections).
  pageErrors: string[];
  // alert()/confirm() messages, auto-dismissed so a dialog never hangs a test.
  dialogs: string[];
};

// Offline guard, installed for every test:
//  - the page and anything else on the local server comes from the real binary;
//  - /config.json and /api/* are answered only from fixtures, and an /api path
//    or device without a fixture is a test failure, never a live call;
//  - the XLSX CDN script is served from node_modules;
//  - every other non-local request is aborted and fails the test.
const test = base.extend<Harness>({
  pageErrors: async ({ page }, use) => {
    const errors: string[] = [];
    page.on('pageerror', (e) => errors.push(String(e)));
    await use(errors);
  },
  dialogs: async ({ page }, use) => {
    const msgs: string[] = [];
    page.on('dialog', (d) => {
      msgs.push(d.message());
      d.dismiss().catch(() => {});
    });
    await use(msgs);
  },
  context: async ({ context }, use) => {
    const violations: string[] = [];
    await context.route('**/*', async (route) => {
      const url = new URL(route.request().url());
      const local = url.hostname === '127.0.0.1' || url.hostname === 'localhost';
      if (local) {
        if (url.pathname === '/config.json') {
          return route.fulfill({ path: path.join(FIXTURE_DIR, 'config.json'), contentType: 'application/json' });
        }
        if (url.pathname.startsWith('/api/')) {
          const name = url.pathname.slice('/api/'.length);
          if (!API_ENDPOINTS.includes(name) || url.searchParams.get('device') !== DEVICE) {
            violations.push('no fixture for ' + url.pathname + url.search);
            return route.fulfill({ status: 599, contentType: 'application/json', body: '{"error":"no e2e fixture"}' });
          }
          return route.fulfill({ path: path.join(FIXTURE_DIR, name + '.json'), contentType: 'application/json' });
        }
        return route.continue();
      }
      if (url.href === XLSX_URL) {
        // crossorigin="anonymous" + SRI on the tag: the response needs CORS.
        return route.fulfill({
          path: XLSX_LOCAL,
          contentType: 'application/javascript',
          headers: { 'Access-Control-Allow-Origin': '*' },
        });
      }
      violations.push('external request blocked: ' + url.href);
      return route.abort('blockedbyclient');
    });
    await use(context);
    expect(violations, 'requests the offline guard had to refuse').toEqual([]);
  },
});

async function search(page: Page) {
  await page.goto('/');
  await page.locator('#device-input').fill(DEVICE);
  await page.locator('#search-btn').click();
  await expect(page.locator('.hero-card .meta')).toContainText('Signals measured:');
}

test('smoke: pacemaker search renders hero, signals and device table', async ({ page, pageErrors }) => {
  const dossier = fixture('dossier');
  const devices = fixture('devices');
  await search(page);

  await expect(page.locator('.hero-card .gauge-center .num')).toHaveText(dossier.attention_index.toFixed(2));
  await expect(page.locator('.hero-card .device-name')).toHaveText(DEVICE);
  const measured = await page.locator('.hero-card .meta').textContent();
  expect(measured).toMatch(new RegExp('^Signals measured: ' + dossier.signals_measured + ' of \\d+$'));

  await expect(page.locator('h2.section', { hasText: 'Device Records' })).toHaveText(
    '📇 Device Records (' + devices.records.length + ')');
  await expect(page.locator(MAIN_ROWS)).toHaveCount(strongRecords(devices).length);
  // The category search fetched fewer records than it matched: the meta line
  // says so, so a device past the first page is not read as absent.
  expect(devices.fetched_category).toBeLessThan(devices.total_category);
  await expect(page.locator('.dev-card .meta').first()).toContainText(
    'category search: first ' + devices.fetched_category.toLocaleString('en-US') +
    ' of ' + devices.total_category.toLocaleString('en-US'));
  await expect(page.locator(SIGNAL_CARD)).toHaveCount(dossier.signals.length);

  // The locally served spreadsheet library loaded (SRI matched) and exports.
  expect(await page.evaluate(() => typeof (window as any).XLSX)).toBe('object');
  const download = page.waitForEvent('download');
  await page.locator('.dev-card button', { hasText: 'Excel' }).click();
  expect((await download).suggestedFilename()).toBe('devicera-devices.xlsx');

  expect(pageErrors).toEqual([]);
});

test('activity labels get a neutral dot, concern labels keep the traffic light', async ({ page }) => {
  await search(page);
  const cards = page.locator(SIGNAL_CARD);
  const n = await cards.count();
  expect(n).toBeGreaterThan(0);

  const seen: string[] = [];
  for (let i = 0; i < n; i++) {
    const card = cards.nth(i);
    const label = ((await card.locator('.chip-row .chip').first().textContent()) || '').trim();
    const code = ((await card.locator('.sig-code').textContent()) || '').trim();
    const dot = ((await card.locator('.status-dot').textContent()) || '').trim();
    seen.push(label);
    if (ACTIVITY_LABELS.includes(label)) {
      expect(dot, code + ' (' + label + ')').toBe('⚪');
    }
    if (label === 'High' || label === 'Critical') {
      expect(dot, code + ' (' + label + ')').toBe('🔴');
    }
  }
  // The fixture must actually exercise both branches.
  expect(seen).toContain('Top');
  expect(seen).toContain('High');
});

test('"Signals measured: N of M" counts only activity signals in M', async ({ page }) => {
  const dossier = fixture('dossier');
  const signals = fixture('signals');
  const m = signals.records.filter((r: any) => ACTIVITY_TYPES.includes(r.signal)).length;
  // Guard the fixture: M must differ from the total, or the check proves nothing.
  expect(m).toBeLessThan(signals.records.length);

  await search(page);
  await expect(page.locator('.hero-card .meta')).toHaveText(
    'Signals measured: ' + dossier.signals_measured + ' of ' + m);
});

test('highlight cards keep their "(Label)" suffix', async ({ page }) => {
  const dossier = fixture('dossier');
  const expected: { value: string; label: string }[] = dossier.highlights.slice(0, 3).map((h: string) => {
    const mm = /^\S+ = ([\d.]+) \((\w+)\): /.exec(h);
    expect(mm, 'fixture highlight format: ' + h).not.toBeNull();
    return { value: parseFloat(mm![1]).toFixed(2), label: mm![2] };
  });
  expect(expected.length).toBeGreaterThan(0);

  await search(page);
  for (let i = 0; i < expected.length; i++) {
    const card = page.locator('.mini-card.area-hl' + (i + 1));
    await expect(card.locator('.mini-title')).toHaveText('⭐ Highlight #' + (i + 1));
    await expect(card.locator('.mini-lead')).toHaveText(
      new RegExp(' — ' + expected[i].value.replace('.', '\\.') + ' \\(' + expected[i].label + '\\)$'));
  }
});

test('signal cards state the sample counted, never an unchecked reliability claim', async ({ page, pageErrors }) => {
  const dossier = fixture('dossier');
  const byType = (t: string) => dossier.signals.find((s: any) => s.signal_type === t);
  // Guard the fixture: it must carry the sample fields, or the check proves nothing.
  for (const t of ['SEVERITY', 'VOLUME']) {
    expect(byType(t)?.sample_size, t + ' sample_size in fixture').toBeGreaterThan(0);
    expect(byType(t)?.sample_band, t + ' sample_band in fixture').toBe('large');
  }
  expect(byType('VOLUME').sample_unit).toBe('peer device types');

  await search(page);

  for (const t of ['SEVERITY', 'VOLUME']) {
    const s = byType(t);
    const card = page.locator(SIGNAL_CARD).filter({ has: page.locator('.sig-code', { hasText: new RegExp('^' + t + '$') }) });
    await expect(card).toHaveCount(1);
    await expect(card.locator('.trust-meta')).toContainText(
      '· Live query · Sample: large (' + s.sample_size.toLocaleString('en-US') + ' ' + s.sample_unit + ')');
    await expect(card.locator('.chip-row .chip').nth(1)).toHaveText('large sample');
  }
  await expect(page.locator(SIGNAL_CARD + ' .trust-meta', { hasText: 'Confidence:' })).toHaveCount(0);

  // Rendered text and tooltips only (not the page source): no claim the code
  // never checks survives anywhere in the results.
  const rendered = await page.evaluate(() => {
    const root = document.getElementById('results')!;
    const titles = Array.from(root.querySelectorAll('[title]')).map((e) => e.getAttribute('title'));
    return root.textContent + '\n' + titles.join('\n');
  });
  for (const claim of ['all feeds responded', 'no missing required fields', 'complete server-side counts', 'partial or lagging data']) {
    expect(rendered, 'unchecked claim: ' + claim).not.toContain(claim);
  }
  expect(rendered).toContain('Sample: how many records were counted — not whether the reading is right.');
  expect(pageErrors).toEqual([]);
});

test('Excel export without the XLSX library alerts instead of throwing', async ({ page, pageErrors, dialogs }) => {
  // Page routes win over the context guard: this one request is aborted.
  await page.route(XLSX_URL, (route) => route.abort('failed'));
  await search(page);
  expect(await page.evaluate(() => typeof (window as any).XLSX)).toBe('undefined');

  await page.locator('.dev-card button', { hasText: 'Excel' }).click();
  await expect.poll(() => dialogs.length).toBe(1);
  expect(dialogs[0]).toBe(
    'Excel export is unavailable right now (the spreadsheet library did not load). Please use CSV or JSON instead.');
  expect(pageErrors).toEqual([]);
});

test('weak matches: main table is strong-only, weak section is collapsed and explains itself', async ({ page, pageErrors }) => {
  const devices = fixture('devices');
  const strong = strongRecords(devices);
  const weak = weakRecords(devices);
  // Guard the fixture: both kinds must be present, or the test proves nothing.
  expect(strong.length).toBeGreaterThan(0);
  expect(weak.length).toBeGreaterThan(0);
  expect(strong.length + weak.length).toBe(devices.records.length);

  await search(page);

  // Main table: exactly the strong rows, by UDI.
  const mainUDIs = (await page.locator(MAIN_ROWS + ' td.udi-cell').allTextContents()).map((s) => s.replace(/\s*↗$/, ''));
  expect(mainUDIs.sort()).toEqual(strong.map((r: any) => r.udi).sort());
  // Category chips are built from the strong rows only.
  await expect(page.locator('.dev-card .cat-chip').first()).toHaveText('All' + strong.length);
  // The header states both counts against the upstream total.
  await expect(page.locator('.dev-card .meta').first()).toContainText(
    strong.length + ' strong + ' + weak.length + ' weak matches of ' + devices.total.toLocaleString('en-US'));

  // Weak section: collapsed by default, its rows not visible.
  const section = page.locator(WEAK_SECTION);
  await expect(section).toHaveCount(1);
  await expect(section.locator('summary')).toHaveText(
    'Weak matches (' + weak.length + ') — matched only through a secondary FDA product code');
  await expect(section).not.toHaveAttribute('open', /.*/);
  await expect(section.locator('tbody tr').first()).toBeHidden();

  // Expands on click; every row carries a reason naming the secondary code.
  await section.locator('summary').click();
  await expect(section).toHaveAttribute('open', '');
  const weakRows = section.locator('tbody tr');
  await expect(weakRows).toHaveCount(weak.length);
  await expect(weakRows.first()).toBeVisible();
  const headers = (await section.locator('thead th').allTextContents()).map((h) => h.replace(/[⇅↑↓]$/, ''));
  const reasonIdx = headers.indexOf('Match Reason');
  expect(reasonIdx, 'weak table headers: ' + headers.join(' | ')).toBeGreaterThanOrEqual(0);
  const reasons = await weakRows.locator('td:nth-child(' + (reasonIdx + 1) + ')').allTextContents();
  expect(reasons).toHaveLength(weak.length);
  for (const r of reasons) expect(r).toContain('Secondary FDA code');

  expect(pageErrors).toEqual([]);
});

test('CSV export carries every row, strong and weak, with a Match Reason column', async ({ page }) => {
  const devices = fixture('devices');
  await search(page);

  const download = page.waitForEvent('download');
  await page.locator('.dev-card button', { hasText: 'CSV' }).click();
  const file = await (await download).path();
  const lines = parseCSV(fs.readFileSync(file!, 'utf8').replace(/^﻿/, ''));
  expect(lines[0].join(',')).toBe('sep=,'); // Excel separator hint
  const header = lines[2];
  const body = lines.slice(3).filter((r) => r.length > 1);

  const matchedOn = header.indexOf('Matched On');
  expect(matchedOn).toBeGreaterThanOrEqual(0);
  expect(header[matchedOn + 1]).toBe('Match Reason');
  expect(body).toHaveLength(devices.records.length);

  const udiIdx = header.indexOf('UDI');
  const reasonIdx = matchedOn + 1;
  const byUDI = new Map(body.map((r) => [r[udiIdx], r[reasonIdx]]));
  for (const rec of devices.records) {
    expect(byUDI.get(rec.udi), 'CSV row for ' + rec.udi).toBe(rec.match_reason);
  }
});

// ---- One failed /api call degrades only its own section ----
//
// Each of the five /api calls passes Caddy forward_auth on its own, so one can
// fail while the others succeed: auth logs showed 3 incidents in 7 days, each
// with 1 of 5 calls getting 503 from the quota check and the rest 200.

type Failure = { status: number; body?: object } | 'drop';

// Answers the named endpoints with `failure` — only the first `times` requests
// to each, when given — and hands every other request to the offline guard.
// Returns the live request count per endpoint.
async function failEndpoints(page: Page, names: string[], failure: Failure, times = Infinity) {
  const hits: Record<string, number> = {};
  await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
    const name = new URL(route.request().url()).pathname.slice('/api/'.length);
    hits[name] = (hits[name] || 0) + 1;
    if (!names.includes(name) || hits[name] > times) return route.fallback();
    if (failure === 'drop') return route.abort('connectionreset');
    return route.fulfill({
      status: failure.status,
      contentType: 'application/json',
      body: JSON.stringify(failure.body || {}),
    });
  });
  return hits;
}

// Runs the search and waits until the page has finished with every response
// (retries included), whatever it ended up rendering.
async function searchSettled(page: Page) {
  await page.goto('/');
  await page.locator('#device-input').fill(DEVICE);
  await page.locator('#search-btn').click();
  await expect(page.locator('#search-btn')).toBeEnabled();
  await expect(page.locator('#results .skeleton-note')).toHaveCount(0);
}

const UNAVAILABLE = '.section-unavailable';
const UNAVAILABLE_TEXT = 'This section is temporarily unavailable.';
const TREND_CARD = '.signal-card .sig-name:has-text("Yearly Trend")';
const PROBLEMS_CARD = '.signal-card .sig-name:has-text("Top Reported Problems")';

// The section each endpoint feeds. The signal cards come from the dossier
// first, so a failed /api/signals alone leaves nothing to mark.
const SECTION_OF: Record<string, string | null> = {
  dossier: 'Device overview',
  signals: null,
  trend: 'Yearly Trend',
  'failure-modes': 'Top Reported Problems',
  devices: 'Device Records',
};

// Everything renders except the section fed by `failed` (none: full page),
// which shows only its title and the unavailable note.
async function expectOnlyDegraded(page: Page, failed: string | null) {
  const title = failed ? SECTION_OF[failed] : null;
  await expect(page.locator('.auth-card')).toHaveCount(0);
  await expect(page.locator('.error-card')).toHaveCount(0);
  if (title) {
    await expect(page.locator(UNAVAILABLE)).toHaveCount(1);
    await expect(page.locator(UNAVAILABLE + ' .status')).toHaveText(title);
    await expect(page.locator(UNAVAILABLE + ' p')).toHaveText(UNAVAILABLE_TEXT);
  } else {
    await expect(page.locator(UNAVAILABLE)).toHaveCount(0);
  }
  await expect(page.locator('.hero-card')).toHaveCount(failed === 'dossier' ? 0 : 1);
  await expect(page.locator(SIGNAL_CARD)).toHaveCount(
    failed === 'dossier' ? fixture('signals').records.length : fixture('dossier').signals.length);
  await expect(page.locator(TREND_CARD)).toHaveCount(failed === 'trend' ? 0 : 1);
  await expect(page.locator(PROBLEMS_CARD)).toHaveCount(failed === 'failure-modes' ? 0 : 1);
  await expect(page.locator('.dev-card')).toHaveCount(failed === 'devices' ? 0 : 1);
}

// A full-page auth card with `title` and none of the results behind it.
async function expectFullPage(page: Page, title: string) {
  await expect(page.locator('.auth-card .status')).toHaveText(title);
  await expect(page.locator('.hero-card')).toHaveCount(0);
  await expect(page.locator(SIGNAL_CARD)).toHaveCount(0);
  await expect(page.locator('.dev-card')).toHaveCount(0);
  await expect(page.locator(UNAVAILABLE)).toHaveCount(0);
}

const QUOTA_503 = { status: 503, body: { error: 'quota service unavailable' } };

for (const ep of API_ENDPOINTS) {
  test('503 on ' + ep + ' (persistent) degrades only that section', async ({ page, pageErrors }) => {
    await failEndpoints(page, [ep], QUOTA_503);
    await searchSettled(page);
    await expectOnlyDegraded(page, ep);
    expect(pageErrors).toEqual([]);
  });
}

test('dropped connection on one endpoint degrades only that section', async ({ page, pageErrors }) => {
  for (const ep of API_ENDPOINTS) {
    await page.unrouteAll();
    await failEndpoints(page, [ep], 'drop');
    await searchSettled(page);
    await expectOnlyDegraded(page, ep);
  }
  expect(pageErrors).toEqual([]);
});

test('one-time 503 is retried and fully renders', async ({ page, pageErrors }) => {
  const hits = await failEndpoints(page, ['devices'], QUOTA_503, 1);
  await searchSettled(page);
  await expectOnlyDegraded(page, null);
  expect(hits['devices']).toBe(2);
  for (const ep of API_ENDPOINTS.filter((e) => e !== 'devices')) expect(hits[ep], ep).toBe(1);
  expect(pageErrors).toEqual([]);
});

test('401 on one endpoint stays full-page', async ({ page }) => {
  await failEndpoints(page, ['trend'], { status: 401, body: { error: 'missing/invalid token' } });
  await searchSettled(page);
  await expectFullPage(page, 'Sign-in required');
});

test('403 limit stays full-page', async ({ page }) => {
  await failEndpoints(page, ['trend'], { status: 403, body: { error: 'quota exceeded', resets_at: '2026-10-01T00:00:00Z' } });
  await searchSettled(page);
  await expectFullPage(page, 'Search limit reached');
});

test('403 app_locked stays full-page', async ({ page }) => {
  await failEndpoints(page, ['trend'], { status: 403, body: { reason: 'app_locked' } });
  await searchSettled(page);
  await expectFullPage(page, 'Not included in your plan');
});

test('503 on all five stays full-page', async ({ page }) => {
  await failEndpoints(page, API_ENDPOINTS, QUOTA_503);
  await searchSettled(page);
  await expectFullPage(page, 'Temporarily unavailable');
});

test('502 is not retried', async ({ page, pageErrors }) => {
  const hits = await failEndpoints(page, ['trend'], { status: 502, body: { error: 'upstream data source failed' } });
  await searchSettled(page);
  await expectOnlyDegraded(page, 'trend');
  expect(hits['trend']).toBe(1);
  expect(pageErrors).toEqual([]);
});
