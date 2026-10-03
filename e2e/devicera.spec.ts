import { test as base, expect, type Page, type Route } from '@playwright/test';
import fs from 'fs';
import path from 'path';

// Recorded once from a live `mdi serve` for "pacemaker" and kept verbatim.
const FIXTURE_DIR = path.join(__dirname, 'fixtures', 'pacemaker');
const DEVICE = 'pacemaker';
const API_ENDPOINTS = ['dossier', 'signals', 'trend', 'failure-modes', 'devices'];

const XLSX_URL = 'https://cdn.jsdelivr.net/npm/xlsx-js-style@1.2.0/dist/xlsx.bundle.js';
const XLSX_LOCAL = require.resolve('xlsx-js-style/dist/xlsx.bundle.js');

// Loaded by the page (with SRI) only when /config.json carries a Supabase config.
const SUPABASE_UMD_URL = 'https://cdn.jsdelivr.net/npm/@supabase/supabase-js@2.117.0/dist/umd/supabase.js';
const SUPABASE_UMD_LOCAL = require.resolve('@supabase/supabase-js/dist/umd/supabase.js');

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
//  - the XLSX and Supabase CDN scripts are served from node_modules;
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
      if (url.href === XLSX_URL || url.href === SUPABASE_UMD_URL) {
        // crossorigin="anonymous" + SRI on the tag: the response needs CORS.
        return route.fulfill({
          path: url.href === XLSX_URL ? XLSX_LOCAL : SUPABASE_UMD_LOCAL,
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
  // 50 rows per page; the pager and the 'x / y rows' count carry the total.
  await expect(page.locator(MAIN_ROWS)).toHaveCount(Math.min(50, strongRecords(devices).length));
  await expect(page.locator('.qcount')).toHaveText(' · ' + strongRecords(devices).length + ' / ' + strongRecords(devices).length + ' rows');
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
  // (the table is paged, 50 per page: walk every page with Next)
  const mainUDIs: string[] = [];
  const nextMain = page.locator('nav[aria-label="Device records pages"]').getByRole('button', { name: 'Next' });
  for (;;) {
    mainUDIs.push(...(await page.locator(MAIN_ROWS + ' td.udi-cell').allTextContents()).map((s) => s.replace(/\s*↗$/, '')));
    if (await nextMain.isDisabled()) break;
    await nextMain.click();
  }
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
  await expect(weakRows).toHaveCount(Math.min(50, weak.length)); // first page of the weak table
  await expect(weakRows.first()).toBeVisible();
  const headers = (await section.locator('thead th').allTextContents()).map((h) => h.replace(/[⇅↑↓]$/, ''));
  const reasonIdx = headers.indexOf('Match Reason');
  expect(reasonIdx, 'weak table headers: ' + headers.join(' | ')).toBeGreaterThanOrEqual(0);
  const reasons = await weakRows.locator('td:nth-child(' + (reasonIdx + 1) + ')').allTextContents();
  expect(reasons).toHaveLength(Math.min(50, weak.length));
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

// ---- Rows that look alike carry the fields that tell them apart ----
//
// Zoll registers one "M SERIES BIPHASIC" per hardware configuration: same
// company and brand, a different DI, model number and description, and the
// same product codes in a different order. Synthetic, so the check does not
// depend on what the recorded fixture happens to contain.

const PC = {
  DPS: 'Electrocardiograph',
  DRO: 'Pacemaker, Cardiac, External Transcutaneous (Non-Invasive)',
  LDD: 'Dc-Defibrillator, Low-Energy, (Including Paddles)',
  MKJ: 'Automated External Defibrillators (Non-Wearable)',
};
const codeSet = (primary: string) => Object.keys(PC).sort().map((code) =>
  ({ code, name: (PC as any)[code], primary: code === primary }));
const zollRow = (udi: string, model: string, desc: string, published: string, primary: string, strength: string, reason: string) => ({
  udi, device_name: 'M SERIES BIPHASIC', company: 'Zoll Medical Corporation', device_class: 'Class II',
  product_category: (PC as any)[primary], registration_status: 'Published', listing_status: 'Not in Commercial Distribution',
  sterilization: 'Non-sterile', latex: 'Not labeled latex-free', last_update: '2026-02-13', matched_on: 'Product category',
  match_strength: strength, match_reason: reason,
  model_number: model, device_description: desc, product_codes: codeSet(primary), publish_date: published,
});
const ZOLL = [
  zollRow('00847946003098', '40010021100163010', 'Defibrillator ,MAIN, EMTD-1,AED,3/5LEAD,BIPHASIC,DC,PC,EL,RC,SM,CM,ENG', '2014-09-19', 'MKJ', 'strong', 'FDA category'),
  zollRow('00847946013035', '60010011100010011', '', '', 'LDD', 'strong', 'FDA category'),
  zollRow('00847946004316', '40021221100123010', 'Defibrillator ,MAIN,MAN-1,12 LEAD,SPO2,BIPHASIC,EL,PACE,RCD,SMR,CDMK,D', '2014-09-19', 'DRO', 'weak', 'Secondary FDA code MKJ: ' + PC.MKJ),
];
const NEW_EXPORT_COLS = ['Model Number', 'Device Description', 'Product Codes', 'First Published'];
const codesText = (r: any) => r.product_codes.map((p: any) => p.code + ': ' + p.name).join('; ');

async function serveZoll(page: Page, records: any[] = ZOLL) {
  await page.route((url) => url.pathname === '/api/devices', (route) => route.fulfill({ json: {
    records, count: records.length, total: records.length, total_brand: 0, total_category: records.length,
    fetched_category: records.length, folded_similar: 0,
  } }));
  await search(page);
}

const headerTexts = async (table: any) =>
  (await table.locator('thead th').allTextContents()).map((h: string) => h.replace(/[⇅↑↓]$/, ''));

test('look-alike rows show model number, description, product codes and first-published date', async ({ page, pageErrors }) => {
  await serveZoll(page);
  const main = page.locator('.dev-card > .dev-table-wrap .dev-table');
  const weakSec = page.locator(WEAK_SECTION);
  await weakSec.locator('summary').click();
  const weakTable = weakSec.locator('.dev-table');

  for (const table of [main, weakTable]) {
    const h = await headerTexts(table);
    for (const c of NEW_EXPORT_COLS) expect(h, 'headers: ' + h.join(' | ')).toContain(c);
    // The two dates sit side by side under names that cannot be confused.
    expect(h[h.indexOf('Last Update') + 1]).toBe('First Published');
  }

  // Three rows, three model numbers: two in the main table, one weak.
  const models: string[] = [];
  for (const table of [main, weakTable]) {
    const idx = (await headerTexts(table)).indexOf('Model Number');
    models.push(...await table.locator('tbody tr td:nth-child(' + (idx + 1) + ')').allTextContents());
  }
  expect(models.sort()).toEqual(ZOLL.map((r) => r.model_number).sort());

  const mh = await headerTexts(main);
  const cell = (row: number, col: string) => main.locator('tbody tr').nth(row).locator('td').nth(mh.indexOf(col));
  const rowOf = async (udi: string) => (await main.locator('tbody td.udi-cell').allTextContents())
    .findIndex((s) => s.replace(/\s*↗$/, '') === udi);
  const full = await rowOf(ZOLL[0].udi);
  const empty = await rowOf(ZOLL[1].udi);

  // Description: the full text lives in the title; the cell clamps it.
  await expect(cell(full, 'Device Description').locator('[title]')).toHaveAttribute('title', ZOLL[0].device_description);
  // Empty values read "—", never blank or "undefined".
  await expect(cell(empty, 'Device Description')).toHaveText('—');
  await expect(cell(empty, 'First Published')).toHaveText('—');
  await expect(cell(full, 'First Published')).toHaveText('2014-09-19');

  // Product codes: chips, the shown-category code first and marked, then the rest sorted.
  const chips = cell(full, 'Product Codes').locator('.pc-chip');
  await expect(chips).toHaveText(['MKJ', 'DPS', 'DRO', 'LDD']);
  await expect(chips.first()).toHaveClass(/\bprimary\b/);
  await expect(cell(full, 'Product Codes').locator('.pc-chip.primary')).toHaveCount(1);
  await expect(chips.nth(1)).toHaveAttribute('title', 'DPS: ' + PC.DPS);

  expect(pageErrors).toEqual([]);
});

test('every export carries the four new columns right after Match Reason, filters applied', async ({ page }) => {
  await serveZoll(page);
  const byUDI = new Map(ZOLL.map((r) => [r.udi, r]));
  const want = (r: any) => [r.model_number, r.device_description, codesText(r), r.publish_date];
  const card = page.locator('.dev-card');
  const grab = async (label: string) => {
    const download = page.waitForEvent('download');
    await card.locator('button', { hasText: label }).click();
    return (await download).path();
  };

  // CSV
  const lines = parseCSV(fs.readFileSync((await grab('CSV'))!, 'utf8').replace(/^﻿/, ''));
  const header = lines[2];
  const reason = header.indexOf('Match Reason');
  expect(header.slice(reason + 1, reason + 5)).toEqual(NEW_EXPORT_COLS);
  const body = lines.slice(3).filter((r) => r.length > 1);
  expect(body).toHaveLength(ZOLL.length);
  for (const r of body) expect(r.slice(reason + 1, reason + 5)).toEqual(want(byUDI.get(r[header.indexOf('UDI')])));

  // JSON
  const json = JSON.parse(fs.readFileSync((await grab('JSON'))!, 'utf8'));
  const keys = Object.keys(json.rows[0]);
  expect(keys.slice(keys.indexOf('Match Reason') + 1, keys.indexOf('Match Reason') + 5)).toEqual(NEW_EXPORT_COLS);
  for (const r of json.rows) expect(NEW_EXPORT_COLS.map((c) => r[c])).toEqual(want(byUDI.get(r['UDI'])));

  // Excel
  const XLSX = require('xlsx-js-style');
  const ws = XLSX.readFile((await grab('Excel'))!).Sheets['Devicera'];
  const aoa: any[][] = XLSX.utils.sheet_to_json(ws, { header: 1, defval: '' });
  const xh = aoa[1];
  const xr = xh.indexOf('Match Reason');
  expect(xh.slice(xr + 1, xr + 5)).toEqual(NEW_EXPORT_COLS);
  for (const r of aoa.slice(2)) expect(r.slice(xr + 1, xr + 5).map(String)).toEqual(want(byUDI.get(String(r[xh.indexOf('UDI')]))));

  // BibTeX: model number as the version, the rest in the note.
  const bib = fs.readFileSync((await grab('BibTeX'))!, 'utf8');
  const entries = bib.split(/\n(?=@misc\{)/).filter((e) => e.startsWith('@misc{'));
  expect(entries).toHaveLength(ZOLL.length);
  for (const r of ZOLL) {
    const e = entries.find((x) => x.startsWith('@misc{UDI-' + r.udi + ','))!;
    expect(e, r.udi).toContain('version      = {' + r.model_number + '}');
    expect(e, r.udi).toContain('product codes: ' + codesText(r));
    if (r.publish_date) expect(e, r.udi).toContain('first published: ' + r.publish_date);
    if (r.device_description) expect(e, r.udi).toContain('description: ' + r.device_description);
  }

  // The text filter still narrows every export — here, to one model number.
  await card.locator('.qfilter').fill(ZOLL[0].model_number);
  const filtered = parseCSV(fs.readFileSync((await grab('CSV'))!, 'utf8').replace(/^﻿/, '')).slice(3).filter((r) => r.length > 1);
  expect(filtered.map((r) => r[header.indexOf('UDI')])).toEqual([ZOLL[0].udi]);
});

// ---- The Weak matches label follows the filters, like "x / y rows" ----
//
// ZOLL plus a second weak row that is Class III, so every filter below leaves
// some but not all weak rows and the label must say "shown / total".

const ZOLL_TWO_WEAK = [...ZOLL, {
  ...zollRow('00847946009991', '40030021100199010', 'Defibrillator ,MAIN,MAN-2,PACE,CDMK', '2015-03-02', 'DRO', 'weak', 'Secondary FDA code MKJ: ' + PC.MKJ),
  device_class: 'Class III',
}];
const WEAK_TOTAL = ZOLL_TWO_WEAK.filter((r) => r.match_strength === 'weak').length;
const weakLabel = (n: string) => 'Weak matches (' + n + ') — matched only through a secondary FDA product code';

test('weak label: text filter that hides every weak row reads "0 / N", clearing restores "N"', async ({ page, pageErrors }) => {
  await serveZoll(page, ZOLL_TWO_WEAK);
  const section = page.locator(WEAK_SECTION);
  const filter = page.locator('.dev-card .qfilter');
  await expect(section.locator('summary')).toHaveText(weakLabel(String(WEAK_TOTAL)));

  await filter.fill(ZOLL[0].model_number); // a strong row
  await expect(section).toHaveCount(1); // the section stays
  await expect(section.locator('summary')).toHaveText(weakLabel('0 / ' + WEAK_TOTAL));
  await expect(section.locator('tbody tr')).toHaveCount(0);

  await filter.fill('');
  await expect(section.locator('summary')).toHaveText(weakLabel(String(WEAK_TOTAL)));
  expect(pageErrors).toEqual([]);
});

test('weak label: text filter to one weak row reads "1 / N"', async ({ page, pageErrors }) => {
  await serveZoll(page, ZOLL_TWO_WEAK);
  const section = page.locator(WEAK_SECTION);
  await page.locator('.dev-card .qfilter').fill(ZOLL[2].model_number);
  await expect(section.locator('summary')).toHaveText(weakLabel('1 / ' + WEAK_TOTAL));
  await expect(section.locator('tbody tr')).toHaveCount(1);
  expect(pageErrors).toEqual([]);
});

test('weak label: Class III toggle counts the visible weak rows', async ({ page, pageErrors }) => {
  await serveZoll(page, ZOLL_TWO_WEAK);
  const section = page.locator(WEAK_SECTION);
  await page.locator('.dev-card .quick-class3').click();
  const visible = await section.locator('tbody tr').count();
  expect(visible).toBe(1);
  await expect(section.locator('summary')).toHaveText(weakLabel(visible + ' / ' + WEAK_TOTAL));
  expect(pageErrors).toEqual([]);
});

// ---- Two chip bars: main chips filter the main table, weak chips the weak one ----
//
// Weak rows never share a category with the strong rows in the recorded data,
// so one shared chip filter dropped every weak row. Each table now has its own
// bar, and each chip count is the rows that chip shows on its own.

const MAIN_CHIPS = '.dev-card > .cat-bar .cat-chip';
const WEAK_CHIPS = WEAK_SECTION + ' .cat-bar .cat-chip';
const udisOf = async (rows: any) => (await rows.locator('td.udi-cell').allTextContents()).map((s: string) => s.replace(/\s*↗$/, ''));

// ZOLL with its weak row plus a second weak row in another category, so the
// weak bar has two chips.
const ZOLL_WEAK_SPLIT = [...ZOLL,
  zollRow('00847946007775', '40040021100177010', 'Defibrillator ,MAIN,MAN-3,12 LEAD,ECG', '2016-05-11', 'DPS', 'weak', 'Secondary FDA code MKJ: ' + PC.MKJ)];

test('chip bars: a main chip leaves the weak table and its label untouched', async ({ page, pageErrors }) => {
  await serveZoll(page, ZOLL_TWO_WEAK);
  const section = page.locator(WEAK_SECTION);
  await section.locator('summary').click();
  const chip = page.locator(MAIN_CHIPS, { hasText: PC.MKJ });
  await chip.click();
  await expect(page.locator(MAIN_ROWS)).toHaveCount(1);
  await expect(section.locator('summary')).toHaveText(weakLabel(String(WEAK_TOTAL)));
  await expect(section.locator('tbody tr')).toHaveCount(WEAK_TOTAL);
  await expect(chip).toHaveClass(/\bactive\b/);
  await expect(chip).toHaveAttribute('aria-pressed', 'true');
  expect(pageErrors).toEqual([]);
});

test('chip bars: a weak chip filters only the weak table and the label reads "1 / 2"', async ({ page, pageErrors }) => {
  await serveZoll(page, ZOLL_WEAK_SPLIT);
  const section = page.locator(WEAK_SECTION);
  await section.locator('summary').click();
  const mainBefore = await page.locator(MAIN_ROWS).count();
  await expect(page.locator(WEAK_CHIPS).first()).toHaveText('All2');
  const chip = page.locator(WEAK_CHIPS, { hasText: PC.DPS });
  await chip.click();
  await expect(chip).toHaveAttribute('aria-pressed', 'true');
  await expect(section.locator('summary')).toHaveText(weakLabel('1 / 2'));
  expect(await udisOf(section.locator('tbody tr'))).toEqual([ZOLL_WEAK_SPLIT[3].udi]);
  await expect(page.locator(MAIN_ROWS)).toHaveCount(mainBefore);
  // Clicking the active chip resets this bar only.
  await chip.click();
  await expect(chip).toHaveAttribute('aria-pressed', 'false');
  await expect(page.locator(WEAK_CHIPS).first()).toHaveAttribute('aria-pressed', 'true');
  await expect(section.locator('summary')).toHaveText(weakLabel('2'));
  expect(pageErrors).toEqual([]);
});

test('chip bars: pacemaker chip counts match the rows each chip shows, in both bars', async ({ page, pageErrors }) => {
  const devices = fixture('devices');
  const strong = strongRecords(devices);
  const weak = weakRecords(devices);
  await search(page);
  const section = page.locator(WEAK_SECTION);
  await section.locator('summary').click();

  await expect(page.locator(MAIN_CHIPS).first()).toHaveText('All' + strong.length);
  await expect(page.locator(WEAK_CHIPS).first()).toHaveText('All' + weak.length);
  const counts = (await page.locator(WEAK_CHIPS + ' .cnt').allTextContents()).map(Number);
  expect(counts.slice(1).reduce((a, b) => a + b, 0)).toBe(weak.length);

  // Every chip, alone, shows exactly its count; the other table does not move.
  for (const [chips, rows, other, otherTotal] of [
    [MAIN_CHIPS, MAIN_ROWS, WEAK_SECTION + ' tbody tr', weak.length],
    [WEAK_CHIPS, WEAK_SECTION + ' tbody tr', MAIN_ROWS, strong.length],
  ] as const) {
    const n = await page.locator(chips).count();
    for (let i = 1; i < n; i++) {
      const chip = page.locator(chips).nth(i);
      const want = Number(await chip.locator('.cnt').textContent());
      await chip.click();
      // Tables are paged (50 rows): a chip shows min(count, 50) rows on page 1.
      await expect(page.locator(rows), 'chip ' + (await chip.textContent())).toHaveCount(Math.min(want, 50));
      await expect(page.locator(other)).toHaveCount(Math.min(otherTotal, 50));
      await chip.click(); // back to All
    }
  }
  expect(pageErrors).toEqual([]);
});

test('chip bars: CSV export after a main chip and a weak chip carries exactly the visible rows', async ({ page, pageErrors }) => {
  await serveZoll(page, ZOLL_WEAK_SPLIT);
  const card = page.locator('.dev-card');
  const section = page.locator(WEAK_SECTION);
  await section.locator('summary').click();
  await page.locator(MAIN_CHIPS, { hasText: PC.MKJ }).click();
  await page.locator(WEAK_CHIPS, { hasText: PC.DPS }).click();
  const visible = [...await udisOf(page.locator(MAIN_ROWS)), ...await udisOf(section.locator('tbody tr'))];
  expect(visible.sort()).toEqual([ZOLL[0].udi, ZOLL_WEAK_SPLIT[3].udi].sort());

  const download = page.waitForEvent('download');
  await card.locator('button', { hasText: 'CSV' }).click();
  const lines = parseCSV(fs.readFileSync((await (await download).path())!, 'utf8').replace(/^﻿/, ''));
  const header = lines[2];
  const udis = lines.slice(3).filter((r) => r.length > 1).map((r) => r[header.indexOf('UDI')]);
  expect(udis.sort()).toEqual(visible.sort());
  expect(pageErrors).toEqual([]);
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

// ---- Several failed /api calls: the trend and problems cards stand alone ----
//
// The two cards read only /api/trend and /api/failure-modes. Without signal
// cards in the Usage & Reports group they get a section of their own, headed
// "Yearly Trend & Top Problems".

const USAGE_FALLBACK_HEADING = '📈 Yearly Trend & Top Problems';

// Every section fed by a failed endpoint shows its unavailable note; the
// trend and problems cards follow their own endpoint only.
async function expectDegraded(page: Page, failed: string[]) {
  const titles = failed.map((ep) => SECTION_OF[ep]).filter((t): t is string => !!t);
  await expect(page.locator('.auth-card')).toHaveCount(0);
  await expect(page.locator('.error-card')).toHaveCount(0);
  await expect(page.locator(UNAVAILABLE)).toHaveCount(titles.length);
  expect((await page.locator(UNAVAILABLE + ' .status').allTextContents()).sort()).toEqual([...titles].sort());
  for (const p of await page.locator(UNAVAILABLE + ' p').allTextContents()) expect(p).toBe(UNAVAILABLE_TEXT);
  const has = (ep: string) => failed.includes(ep);
  await expect(page.locator('.hero-card')).toHaveCount(has('dossier') ? 0 : 1);
  await expect(page.locator(SIGNAL_CARD)).toHaveCount(
    has('dossier') && has('signals') ? 0
      : has('dossier') ? fixture('signals').records.length : fixture('dossier').signals.length);
  await expect(page.locator(TREND_CARD)).toHaveCount(has('trend') ? 0 : 1);
  await expect(page.locator(PROBLEMS_CARD)).toHaveCount(has('failure-modes') ? 0 : 1);
  await expect(page.locator('.dev-card')).toHaveCount(has('devices') ? 0 : 1);
}

test('503 on dossier + signals keeps the trend and problems cards', async ({ page, pageErrors }) => {
  await failEndpoints(page, ['dossier', 'signals'], QUOTA_503);
  await searchSettled(page);
  await expectDegraded(page, ['dossier', 'signals']);
  await expect(page.locator('h2.section', { hasText: 'Yearly Trend & Top Problems' })).toHaveText(USAGE_FALLBACK_HEADING);
  await expect(page.locator(SLOT('usage') + ' .signal-card')).toHaveCount(2); // trend + problems, in their own slot
  expect(pageErrors).toEqual([]);
});

test('503 on dossier + signals + trend marks Yearly Trend unavailable on its own', async ({ page, pageErrors }) => {
  await failEndpoints(page, ['dossier', 'signals', 'trend'], QUOTA_503);
  await searchSettled(page);
  await expectDegraded(page, ['dossier', 'signals', 'trend']);
  await expect(page.locator('h2.section', { hasText: 'Yearly Trend & Top Problems' })).toHaveText(USAGE_FALLBACK_HEADING);
  await expect(page.locator(SLOT('usage') + ' .signal-card')).toHaveCount(2); // trend + problems, in their own slot
  expect(pageErrors).toEqual([]);
});

test('no usage-group signals still shows the trend and problems cards', async ({ page, pageErrors }) => {
  // The fixtures minus the three Usage & Reports signal types.
  const USAGE_TYPES = ['SEVERITY', 'VOLUME', 'VOLUME_SHIFT'];
  const dossier = fixture('dossier');
  dossier.signals = dossier.signals.filter((s: any) => !USAGE_TYPES.includes(s.signal_type));
  const signals = fixture('signals');
  signals.records = signals.records.filter((s: any) => !USAGE_TYPES.includes(s.signal));
  expect(dossier.signals.length).toBeGreaterThan(0);
  await page.route((url) => url.pathname === '/api/dossier' || url.pathname === '/api/signals', (route) =>
    route.fulfill({ json: new URL(route.request().url()).pathname === '/api/dossier' ? dossier : signals }));
  await searchSettled(page);
  await expect(page.locator('.auth-card')).toHaveCount(0);
  await expect(page.locator(UNAVAILABLE)).toHaveCount(0);
  await expect(page.locator(SIGNAL_CARD)).toHaveCount(dossier.signals.length);
  await expect(page.locator(TREND_CARD)).toHaveCount(1);
  await expect(page.locator(PROBLEMS_CARD)).toHaveCount(1);
  await expect(page.locator('h2.section', { hasText: 'Yearly Trend & Top Problems' })).toHaveText(USAGE_FALLBACK_HEADING);
  await expect(page.locator(SLOT('usage') + ' .signal-card')).toHaveCount(2); // trend + problems, in their own slot
  expect(pageErrors).toEqual([]);
});

test('normal search has exactly one "Usage & Reports" heading: the signal group, not the trend slot', async ({ page, pageErrors }) => {
  await searchSettled(page);
  const starts = await page.evaluate(() => Array.from(document.querySelectorAll('#results *'))
    .filter((el) => el.children.length === 0 && (el.textContent || '').trim().startsWith('📊 Usage & Reports'))
    .map((el) => (el.textContent || '').trim()));
  expect(starts).toHaveLength(1);
  expect(starts[0]).toMatch(/^📊 Usage & Reports \(\d+\)$/);
  await expect(page.locator(SLOT('usage') + ' h2.section')).toHaveText(USAGE_FALLBACK_HEADING);
  expect(pageErrors).toEqual([]);
});

// Sign-in. The fixture config is empty, so these tests hand the page a
// Supabase config and an unexpired session in localStorage; supabase-js reads
// that session without a network call, so nothing reaches *.supabase.co.
const SUPA_REF = 'abcdefghijklmnopqrst';
const SIGNIN_UNAVAILABLE = 'Sign-in is unavailable: a required script did not load.';

async function configureAuth(page: Page): Promise<string[]> {
  await page.route((url) => url.pathname === '/config.json', (route) =>
    route.fulfill({ json: { supabase_url: `https://${SUPA_REF}.supabase.co`, supabase_anon_key: 'pk_e2e' } }));
  const session = {
    access_token: 'E2E.ACCESS.TOKEN', refresh_token: 'r', token_type: 'bearer', expires_in: 3600,
    expires_at: Math.floor(Date.now() / 1000) + 3600,
    user: { id: 'u1', email: 'tester@example.com', aud: 'authenticated' },
  };
  await page.addInitScript(([k, v]) => localStorage.setItem(k, v),
    [`sb-${SUPA_REF}-auth-token`, JSON.stringify(session)]);
  // The Authorization header of every /api request, '' when absent.
  const auth: string[] = [];
  page.on('request', (r) => {
    if (new URL(r.url()).pathname.startsWith('/api/')) auth.push(r.headers()['authorization'] || '');
  });
  return auth;
}

test('sign-in: configured page with a session sends Bearer on every /api call', async ({ page, pageErrors }) => {
  const auth = await configureAuth(page);
  await search(page);
  await expect(page.locator('#auth-bar')).toBeVisible();
  await expect(page.locator('#auth-email')).toHaveText('tester@example.com');
  expect(auth).toHaveLength(API_ENDPOINTS.length);
  for (const h of auth) expect(h).toBe('Bearer E2E.ACCESS.TOKEN');
  expect(pageErrors).toEqual([]);
});

// Script failed: the bar carries only the message, no session UI, no token,
// and the 401 card says why instead of offering a sign-in that cannot work.
async function expectSignInUnavailable(page: Page, auth: string[]) {
  // Caddy's forward_auth stand-in: no token, 401.
  await page.route((url) => url.pathname.startsWith('/api/'), (route) =>
    route.request().headers()['authorization']
      ? route.fallback()
      : route.fulfill({ status: 401, json: { error: 'missing/invalid token' } }));
  await page.goto('/');
  await expect(page.locator('#auth-bar')).toBeVisible();
  await expect(page.locator('#auth-bar')).toHaveJSProperty('innerText', SIGNIN_UNAVAILABLE);
  await expect(page.locator('#auth-email')).toBeHidden();
  await expect(page.locator('#auth-signin-btn')).toBeHidden();
  await expect(page.locator('#auth-signout-btn')).toBeHidden();
  await page.locator('#device-input').fill(DEVICE);
  await page.locator('#search-btn').click();
  await expect(page.locator('.auth-card p')).toHaveText(SIGNIN_UNAVAILABLE);
  await expect(page.locator('.auth-card button')).toHaveCount(0);
  expect(auth.length).toBeGreaterThan(0);
  for (const h of auth) expect(h).not.toContain('Bearer');
}

test('sign-in: tampered Supabase script is refused by SRI', async ({ page, pageErrors }) => {
  const auth = await configureAuth(page);
  const body = fs.readFileSync(SUPABASE_UMD_LOCAL, 'utf8') + '\n/* tampered */\n';
  await page.route(SUPABASE_UMD_URL, (route) => route.fulfill({
    body, contentType: 'application/javascript', headers: { 'Access-Control-Allow-Origin': '*' } }));
  await expectSignInUnavailable(page, auth);
  expect(pageErrors).toEqual([]);
});

test('sign-in: Supabase script that fails to load explains itself', async ({ page, pageErrors }) => {
  const auth = await configureAuth(page);
  await page.route(SUPABASE_UMD_URL, (route) => route.abort('failed'));
  await expectSignInUnavailable(page, auth);
  expect(pageErrors).toEqual([]);
});

test('502 is not retried', async ({ page, pageErrors }) => {
  const hits = await failEndpoints(page, ['trend'], { status: 502, body: { error: 'upstream data source failed' } });
  await searchSettled(page);
  await expectOnlyDegraded(page, 'trend');
  expect(hits['trend']).toBe(1);
  expect(pageErrors).toEqual([]);
});


// ---- Rate-limit banner and header text (option C, B2) ----
//
// The Go side marks an openFDA 429 as "rate_limited": true on a 200 body, or as
// code "rate_limited" on a 502. The page turns either into ONE banner above the
// results; the sections keep their own unavailable note, and nothing is retried.

const BANNER_TEXT =
  'The FDA data service is limiting requests right now. Some results may be missing — wait a minute and search again.';
const BANNER = '#rate-limit-banner';
const SUBTITLE_TEXT =
  'Search medical device registrations, UDI codes, and FDA regulatory status — public FDA, ClinicalTrials.gov and PubMed data.';

// Answers /api/<name> with the recorded fixture plus `extra` top-level fields.
async function markEndpoint(page: Page, name: string, extra: object) {
  await page.route((url) => url.pathname === '/api/' + name, (route) =>
    route.fulfill({ json: { ...fixture(name), ...extra } }));
}

test('E1 a rate_limited trend body shows one banner and the other sections render', async ({ page, pageErrors }) => {
  await markEndpoint(page, 'trend', { rate_limited: true });
  await searchSettled(page);
  await expect(page.getByText(BANNER_TEXT, { exact: true })).toHaveCount(1);
  await expect(page.locator(BANNER)).toHaveCount(1);
  await expect(page.locator(BANNER)).toHaveText(BANNER_TEXT);
  await expect(page.locator(BANNER)).toBeVisible();
  // The banner sits directly above the results.
  await expect(page.locator(BANNER + ' + #results')).toHaveCount(1);
  await expectOnlyDegraded(page, null);
  expect(pageErrors).toEqual([]);
});

test('E2 a 502 rate_limited on failure-modes shows the banner, the section note, and no retry', async ({ page, pageErrors }) => {
  const hits = await failEndpoints(page, ['failure-modes'], {
    status: 502, body: { error: 'upstream data source failed', code: 'rate_limited' } });
  await searchSettled(page);
  await expect(page.getByText(BANNER_TEXT, { exact: true })).toHaveCount(1);
  await expect(page.locator(BANNER)).toHaveCount(1);
  await expectOnlyDegraded(page, 'failure-modes');
  expect(hits['failure-modes']).toBe(1);
  expect(pageErrors).toEqual([]);
});

test('E3 a 502 upstream_unavailable shows no banner', async ({ page, pageErrors }) => {
  const hits = await failEndpoints(page, ['failure-modes'], {
    status: 502, body: { error: 'upstream data source failed', code: 'upstream_unavailable' } });
  await searchSettled(page);
  await expect(page.locator(BANNER)).toHaveCount(0);
  await expect(page.getByText(BANNER_TEXT, { exact: true })).toHaveCount(0);
  await expectOnlyDegraded(page, 'failure-modes');
  expect(hits['failure-modes']).toBe(1);
  expect(pageErrors).toEqual([]);
});

test('E4 a normal search has no banner, and a normal search after a rate-limited one clears it', async ({ page, pageErrors }) => {
  // Normal first.
  await searchSettled(page);
  await expect(page.locator(BANNER)).toHaveCount(0);
  await expectOnlyDegraded(page, null);

  // Rate-limited: the trend answers with the marker...
  const isTrend = (url: URL) => url.pathname === '/api/trend';
  await page.route(isTrend, (route) =>
    route.fulfill({ json: { ...fixture('trend'), rate_limited: true } }));
  await page.locator('#search-btn').click();
  await expect(page.locator('#search-btn')).toBeEnabled();
  await expect(page.locator('#results .skeleton-note')).toHaveCount(0);
  await expect(page.locator(BANNER)).toHaveCount(1);
  await expect(page.locator(BANNER)).toHaveText(BANNER_TEXT);

  // ...then a normal one: the banner is gone and the page is whole again.
  await page.unroute(isTrend);
  await page.locator('#search-btn').click();
  await expect(page.locator('#search-btn')).toBeEnabled();
  await expect(page.locator('#results .skeleton-note')).toHaveCount(0);
  await expect(page.locator(BANNER)).toHaveCount(0);
  await expect(page.getByText(BANNER_TEXT, { exact: true })).toHaveCount(0);
  await expectOnlyDegraded(page, null);
  expect(pageErrors).toEqual([]);
});

test('E5 subtitle has the new text and the page no longer says keyless', async ({ page, pageErrors }) => {
  await page.goto('/');
  await expect(page.locator('.subtitle')).toHaveText(SUBTITLE_TEXT);
  expect(await page.content()).not.toMatch(/keyless/i);
  await expect(page.locator('meta[name="description"]')).toHaveAttribute('content', /public FDA, ClinicalTrials\.gov and PubMed data/);
  await expect(page.locator('body')).toContainText('public APIs (openFDA with an optional API key)');
  expect(pageErrors).toEqual([]);
});


// ---- Progressive sections (O7): each slot renders when its endpoint is final ----
//
// Placeholders reuse the real card classes plus .skeleton, so "real content"
// below means the same selector minus .skeleton.

const HERO = '.hero-card:not(.skeleton)';
const DEV_CARD = '.dev-card:not(.skeleton)';
const SLOT = (name: string) => '[data-slot="' + name + '"]';
const SR_STATUS = '#sr-status';

type Gate = { wait: Promise<void>; open: () => void };
function gate(): Gate {
  let open!: () => void;
  const wait = new Promise<void>((r) => { open = r; });
  return { wait, open };
}

type Handler = (route: Route, n: number) => Promise<void> | void;

// Per-endpoint handlers; an endpoint without one falls back to the offline
// guard's fixture. `n` is the 1-based request count of that endpoint.
// Returns the live request counts.
async function scripted(page: Page, handlers: Record<string, Handler>) {
  const calls: Record<string, number> = {};
  await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
    const name = new URL(route.request().url()).pathname.slice('/api/'.length);
    calls[name] = (calls[name] || 0) + 1;
    try {
      const h = handlers[name];
      if (h) await h(route, calls[name]);
      else await route.fallback();
    } catch { /* the page went away while the response was held */ }
  });
  return calls;
}

// Holds the named endpoints until their gate opens, then serves the fixture.
function held(names: string[]) {
  const gates: Record<string, Gate> = {};
  const handlers: Record<string, Handler> = {};
  for (const n of names) {
    gates[n] = gate();
    handlers[n] = async (route) => { await gates[n].wait; await route.fallback(); };
  }
  return { gates, handlers, open: (...ns: string[]) => ns.forEach((n) => gates[n].open()) };
}

const status503: Handler = (r) => r.fulfill({ status: 503, contentType: 'application/json', body: '{}' });

async function startSearch(page: Page) {
  await page.goto('/');
  await page.locator('#device-input').fill(DEVICE);
  await page.locator('#search-btn').click();
}

// Collects the path of every finished /api response of this page.
function countResponses(page: Page) {
  const seen: string[] = [];
  page.on('response', (r) => {
    const p = new URL(r.url()).pathname;
    if (p.startsWith('/api/')) seen.push(p);
  });
  return seen;
}

test('P1 sections appear in endpoint order: problems and trend first, hero last', async ({ page, pageErrors }) => {
  const h = held(['dossier', 'signals', 'devices']);
  await scripted(page, h.handlers);
  await startSearch(page);
  await expect(page.locator(PROBLEMS_CARD)).toHaveCount(1);
  await expect(page.locator(TREND_CARD)).toHaveCount(1);
  await expect(page.locator(HERO)).toHaveCount(0);
  await expect(page.locator(DEV_CARD)).toHaveCount(0);
  await expect(page.locator('#search-btn')).toBeDisabled();
  h.open('devices');
  await expect(page.locator(DEV_CARD)).toHaveCount(1);
  await expect(page.locator(HERO)).toHaveCount(0);
  h.open('dossier', 'signals');
  await expect(page.locator(HERO)).toHaveCount(1);
  await expect(page.locator('#search-btn')).toBeEnabled();
  expect(pageErrors).toEqual([]);
});

test('P2 the first 401 replaces the page even with the other four delayed; nothing is painted afterwards', async ({ page, pageErrors }) => {
  const h = held(['dossier', 'signals', 'failure-modes', 'devices']);
  const seen = countResponses(page);
  await scripted(page, {
    ...h.handlers,
    trend: (route) => route.fulfill({ status: 401, contentType: 'application/json', body: '{"error":"missing/invalid token"}' }),
  });
  await startSearch(page);
  await expectFullPage(page, 'Sign-in required');
  await expect(page.locator('.auth-card')).toHaveAttribute('role', 'alert');
  await expect(page.locator('#search-btn')).toBeEnabled();
  h.open('dossier', 'signals', 'failure-modes', 'devices');
  await expect.poll(() => seen.length).toBe(5);
  await page.waitForTimeout(300);
  await expectFullPage(page, 'Sign-in required');
  await expect(page.locator(PROBLEMS_CARD)).toHaveCount(0);
  await expect(page.locator('#rate-limit-banner')).toHaveCount(0);
  expect(pageErrors).toEqual([]);
});

for (const [status, body, title] of [
  [401, { error: 'missing/invalid token' }, 'Sign-in required'],
  [403, { error: 'quota exceeded', resets_at: '2026-10-01T00:00:00Z' }, 'Search limit reached'],
] as const) {
  test('P3 a late ' + status + ' clears the sections that were already painted', async ({ page, pageErrors }) => {
    const h = held(['trend']);
    await scripted(page, {
      trend: async (route) => {
        await h.gates.trend.wait;
        await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
      },
    });
    await startSearch(page);
    await expect(page.locator(HERO)).toHaveCount(1);
    await expect(page.locator(DEV_CARD)).toHaveCount(1);
    await expect(page.locator(PROBLEMS_CARD)).toHaveCount(1);
    h.open('trend');
    await expectFullPage(page, title);
    await expect(page.locator(PROBLEMS_CARD)).toHaveCount(0);
    await expect(page.locator('#search-btn')).toBeEnabled();
    expect(pageErrors).toEqual([]);
  });
}

test('P4 four 503 + one pending stays degraded; the fifth 503 flips to the full-page card', async ({ page, pageErrors }) => {
  const h = held(['devices']);
  await scripted(page, {
    dossier: status503, signals: status503, trend: status503, 'failure-modes': status503,
    devices: async (r, n) => { await h.gates.devices.wait; await status503(r, n); },
  });
  await startSearch(page);
  // Dossier, trend and problems are final (after their 1 s retry) and say so.
  await expect(page.locator(UNAVAILABLE + ':not(.skeleton)')).toHaveCount(3, { timeout: 8000 });
  await expect(page.locator('.auth-card')).toHaveCount(0);
  await expect(page.locator('#search-btn')).toBeDisabled();
  h.open('devices');
  await expectFullPage(page, 'Temporarily unavailable');
  await expect(page.locator('#search-btn')).toBeEnabled();
  expect(pageErrors).toEqual([]);
});

test('P4 four 503 + one 200 stays degraded', async ({ page, pageErrors }) => {
  const h = held(['devices']);
  await scripted(page, { dossier: status503, signals: status503, trend: status503, 'failure-modes': status503, ...h.handlers });
  await startSearch(page);
  await expect(page.locator(UNAVAILABLE + ':not(.skeleton)')).toHaveCount(3, { timeout: 8000 });
  h.open('devices');
  await expect(page.locator(DEV_CARD)).toHaveCount(1);
  await expect(page.locator('#search-btn')).toBeEnabled();
  await expect(page.locator('.auth-card')).toHaveCount(0);
  await expect(page.locator('.error-card')).toHaveCount(0);
  expect(pageErrors).toEqual([]);
});

test('P4 all five dropped connections show the network card with role alert', async ({ page, pageErrors }) => {
  await failEndpoints(page, API_ENDPOINTS, 'drop');
  await searchSettled(page);
  await expect(page.locator('.error-card .status')).toHaveText('Network error');
  await expect(page.locator('.error-card')).toHaveAttribute('role', 'alert');
  await expect(page.locator(UNAVAILABLE)).toHaveCount(0);
  expect(pageErrors).toEqual([]);
});

test('P5 a stale response of an older search is ignored (page and CSV show only the newer one)', async ({ page, pageErrors }) => {
  const devices = fixture('devices');
  const g = gate();
  const seen = countResponses(page);
  const stale = (make: () => any): Handler => async (route, n) => {
    if (n === 1) {
      await g.wait;
      return route.fulfill({ json: make() });
    }
    return route.fallback();
  };
  await scripted(page, {
    dossier: stale(() => ({ ...fixture('dossier'), device: 'STALE-A' })),
    signals: stale(() => fixture('signals')),
    trend: stale(() => fixture('trend')),
    'failure-modes': stale(() => fixture('failure-modes')),
    devices: stale(() => ({ ...devices, records: devices.records.slice(0, 1), total: 1 })),
  });
  await startSearch(page); // search A: everything held
  await page.locator('#device-input').press('Enter'); // search B while A is pending
  await expect(page.locator(HERO)).toHaveCount(1);
  await expect(page.locator(DEV_CARD)).toHaveCount(1);
  await expect(page.locator(HERO + ' .device-name')).toHaveText(DEVICE);
  g.open();
  await expect.poll(() => seen.length).toBe(10);
  await page.waitForTimeout(300);
  await expect(page.locator(HERO + ' .device-name')).toHaveText(DEVICE);
  await expect(page.locator('h2.section', { hasText: 'Device Records' })).toHaveText('📇 Device Records (' + devices.records.length + ')');
  const download = page.waitForEvent('download');
  await page.locator(DEV_CARD + ' button', { hasText: 'CSV' }).click();
  const file = await (await download).path();
  const lines = parseCSV(fs.readFileSync(file!, 'utf8').replace(/^﻿/, ''));
  expect(lines.slice(3).filter((r) => r.length > 1)).toHaveLength(devices.records.length);
  expect(pageErrors).toEqual([]);
});

test('P6 a 503 keeps its section pending for the 1 s retry while the others render, then it renders', async ({ page, pageErrors }) => {
  const calls = await scripted(page, {
    devices: (route, n) => (n === 1 ? status503(route, n) : route.fallback()),
  });
  await startSearch(page);
  const t0 = Date.now();
  await expect(page.locator(HERO)).toHaveCount(1);
  await expect(page.locator(DEV_CARD)).toHaveCount(0);
  await expect(page.locator(SLOT('devices'))).toHaveAttribute('aria-busy', 'true');
  await expect(page.locator(DEV_CARD)).toHaveCount(1);
  expect(Date.now() - t0).toBeGreaterThanOrEqual(500); // t0 is taken after the click, the 1 s delay started before it
  expect(calls['devices']).toBe(2);
  expect(pageErrors).toEqual([]);
});

test('P6 a new search during the 1 s retry does not fire the old retry', async ({ page, pageErrors }) => {
  const calls = await scripted(page, {
    devices: (route, n) => (n === 1 ? status503(route, n) : route.fallback()),
  });
  await startSearch(page);
  await page.waitForTimeout(300); // inside the 1 s retry delay
  await page.locator('#device-input').press('Enter'); // search B
  await expect(page.locator(DEV_CARD)).toHaveCount(1);
  await page.waitForTimeout(1500); // past where A's retry would fire
  expect(calls['devices']).toBe(2); // A's first try + B; no retry of A
  expect(pageErrors).toEqual([]);
});

test('P7 the banner appears on the first limited final response, stays, and clears on a new search', async ({ page, pageErrors }) => {
  const h = held(['dossier', 'signals']);
  const g2 = gate();
  await scripted(page, {
    trend: (route) => route.fulfill({ json: { ...fixture('trend'), rate_limited: true } }),
    dossier: async (route, n) => { await (n === 1 ? h.gates.dossier : g2).wait; await route.fallback(); },
    signals: async (route, n) => { await (n === 1 ? h.gates.signals : g2).wait; await route.fallback(); },
  });
  await startSearch(page);
  await expect(page.locator(BANNER)).toHaveCount(1);
  await expect(page.locator(HERO)).toHaveCount(0);
  h.open('dossier', 'signals');
  await expect(page.locator(HERO)).toHaveCount(1);
  await expect(page.locator(BANNER)).toHaveCount(1);
  await expect(page.locator('#search-btn')).toBeEnabled();
  await page.locator('#search-btn').click(); // new search, dossier held again
  await expect(page.locator(BANNER)).toHaveCount(1); // trend of the new search is limited again: back at once
  g2.open();
  await expect(page.locator('#search-btn')).toBeEnabled();
  expect(pageErrors).toEqual([]);
});

test('P7 a new search clears the banner at its start when nothing of it is limited', async ({ page, pageErrors }) => {
  let limited = true;
  const g2 = gate();
  await scripted(page, {
    trend: (route) => route.fulfill({ json: limited ? { ...fixture('trend'), rate_limited: true } : fixture('trend') }),
    dossier: async (route) => { if (!limited) await g2.wait; await route.fallback(); },
  });
  await startSearch(page);
  await expect(page.locator(BANNER)).toHaveCount(1);
  await expect(page.locator('#search-btn')).toBeEnabled();
  limited = false;
  await page.locator('#search-btn').click(); // dossier held: the search is still running
  await expect(page.locator(PROBLEMS_CARD)).toHaveCount(1);
  await expect(page.locator(BANNER)).toHaveCount(0);
  g2.open();
  await expect(page.locator('#search-btn')).toBeEnabled();
  await expect(page.locator(BANNER)).toHaveCount(0);
  expect(pageErrors).toEqual([]);
});

test('P8 export buttons exist only for painted cards and the CSV matches the rows after a progressive paint', async ({ page, pageErrors }) => {
  const devices = fixture('devices');
  const h = held(['dossier', 'signals', 'trend', 'failure-modes']);
  await scripted(page, h.handlers);
  await startSearch(page);
  await expect(page.locator(DEV_CARD)).toHaveCount(1);
  await expect(page.locator('button', { hasText: 'CSV' })).toHaveCount(1);
  await expect(page.locator(HERO)).toHaveCount(0);
  const download = page.waitForEvent('download');
  await page.locator(DEV_CARD + ' button', { hasText: 'CSV' }).click();
  const file = await (await download).path();
  const lines = parseCSV(fs.readFileSync(file!, 'utf8').replace(/^﻿/, ''));
  expect(lines.slice(3).filter((r) => r.length > 1)).toHaveLength(devices.records.length);
  h.open('dossier', 'signals', 'trend', 'failure-modes');
  await expect(page.locator(HERO)).toHaveCount(1);
  await expect(page.locator('button', { hasText: 'CSV' })).toHaveCount(1);
  expect(pageErrors).toEqual([]);
});

test('P9 the search button stays disabled until all five responses are final', async ({ page, pageErrors }) => {
  const h = held(['dossier']);
  await scripted(page, h.handlers);
  await startSearch(page);
  await expect(page.locator(PROBLEMS_CARD)).toHaveCount(1);
  await expect(page.locator(DEV_CARD)).toHaveCount(1);
  await expect(page.locator('#search-btn')).toBeDisabled();
  h.open('dossier');
  await expect(page.locator('#search-btn')).toBeEnabled();
  await expect(page.locator('#results .skeleton-note')).toHaveCount(0);
  expect(pageErrors).toEqual([]);
});

test('P10 filling a slot does not change its height by more than the tolerance', async ({ page, pageErrors }) => {
  // Device Records is paginated (50 rows), so its slot can reserve page 1.
  // The signal group count varies.
  const TOL: Record<string, number> = { overview: 24, signals: 120, devices: 60, trend: 24, problems: 24 };
  const names = ['overview', 'signals', 'devices', 'trend', 'problems'];
  const h = held(API_ENDPOINTS);
  await scripted(page, h.handlers);
  await startSearch(page);
  const heights = async () => Promise.all(names.map((n) =>
    page.locator(SLOT(n)).evaluate((el) => el.getBoundingClientRect().height)));
  for (const n of names) await expect(page.locator(SLOT(n))).toHaveAttribute('aria-busy', 'true');
  const pending = await heights();
  h.open(...API_ENDPOINTS);
  await expect(page.locator('#search-btn')).toBeEnabled();
  await page.waitForTimeout(700); // the fade-in animation
  const filled = await heights();
  const report = names.map((n, i) => n + ' pending ' + Math.round(pending[i]) + ' filled ' + Math.round(filled[i]));
  names.forEach((n, i) => {
    expect(Math.abs(filled[i] - pending[i]), report.join(' | ')).toBeLessThanOrEqual(TOL[n]);
  });
  expect(pageErrors).toEqual([]);
});

test('P11 aria-busy goes true to false per slot, one status message per section, focus stays put', async ({ page, pageErrors }) => {
  await page.addInitScript(() => {
    (window as any).__sr = [];
    document.addEventListener('DOMContentLoaded', () => {
      const el = document.getElementById('sr-status');
      if (!el) return;
      new MutationObserver((records) => {
        for (const r of records) r.addedNodes.forEach((n) => { if (n.textContent) (window as any).__sr.push(n.textContent); });
      }).observe(el, { childList: true });
    });
  });
  const h = held(['dossier', 'signals']);
  await scripted(page, h.handlers);
  await startSearch(page);
  await expect(page.locator(SLOT('trend'))).toHaveAttribute('aria-busy', 'false');
  await expect(page.locator(SLOT('problems'))).toHaveAttribute('aria-busy', 'false');
  await expect(page.locator(SLOT('devices'))).toHaveAttribute('aria-busy', 'false');
  await expect(page.locator(SLOT('overview'))).toHaveAttribute('aria-busy', 'true');
  await expect(page.locator(SLOT('signals'))).toHaveAttribute('aria-busy', 'true');
  const focusBefore = await page.evaluate(() => document.activeElement && document.activeElement.tagName + '#' + document.activeElement.id);
  h.open('dossier', 'signals');
  await expect(page.locator(SLOT('overview'))).toHaveAttribute('aria-busy', 'false');
  await expect(page.locator(SLOT('signals'))).toHaveAttribute('aria-busy', 'false');
  await expect(page.locator(SR_STATUS + ' > div').last()).toHaveText('All sections loaded');
  await expect(page.locator(SR_STATUS)).toHaveAttribute('role', 'status');
  await expect(page.locator(SR_STATUS)).toHaveAttribute('aria-live', 'polite');
  await expect(page.locator('#results ' + SR_STATUS)).toHaveCount(0);
  expect(await page.evaluate(() => document.activeElement && document.activeElement.tagName + '#' + document.activeElement.id)).toBe(focusBefore);
  const said: string[] = await page.evaluate(() => (window as any).__sr);
  expect([...said].sort()).toEqual([
    'All sections loaded', 'Device Records loaded', 'Device overview loaded',
    'Signal readings loaded', 'Top Reported Problems loaded', 'Yearly Trend loaded',
  ].sort());
  expect(pageErrors).toEqual([]);
});

test('P12 trend and problems render under Usage & Reports while the dossier is delayed', async ({ page, pageErrors }) => {
  const h = held(['dossier', 'signals']);
  await scripted(page, h.handlers);
  await startSearch(page);
  await expect(page.locator(SLOT('usage') + ' h2.section')).toHaveText(USAGE_FALLBACK_HEADING);
  await expect(page.locator(SLOT('usage') + ' ' + TREND_CARD)).toHaveCount(1);
  await expect(page.locator(SLOT('usage') + ' ' + PROBLEMS_CARD)).toHaveCount(1);
  await expect(page.locator(HERO)).toHaveCount(0);
  h.open('dossier', 'signals');
  await expect(page.locator(HERO)).toHaveCount(1);
  await expect(page.locator(SLOT('usage') + ' ' + TREND_CARD)).toHaveCount(1);
  expect(pageErrors).toEqual([]);
});


// ---- Device Records pagination (O7 phase C): 50 rows per page, per table ----

const PAGE_SIZE = 50;
const PAGER = 'nav[aria-label="Device records pages"]';
const WEAK_PAGER = WEAK_SECTION + ' nav[aria-label="Weak matches pages"]';
const PAGER_INFO = '.pager-info';
const MAIN_UDIS = MAIN_ROWS + ' td.udi-cell';
const stripUdi = (ss: string[]) => ss.map((s) => s.replace(/\s*↗$/, ''));
const pageInfo = (from: number, to: number, of: number, pages: number, page: number) =>
  'Page ' + page + ' of ' + pages + ' · rows ' + from + '–' + to + ' of ' + of;

test('Q1 the strong table shows 50 rows per page: Next shows rows 51-100, Previous goes back', async ({ page, pageErrors }) => {
  const strong = strongRecords(fixture('devices'));
  const pages = Math.ceil(strong.length / PAGE_SIZE);
  await search(page);
  await expect(page.locator(MAIN_ROWS)).toHaveCount(PAGE_SIZE);
  expect(stripUdi(await page.locator(MAIN_UDIS).allTextContents())).toEqual(strong.slice(0, 50).map((r: any) => r.udi));
  await expect(page.locator(PAGER + ' ' + PAGER_INFO)).toHaveText(pageInfo(1, 50, strong.length, pages, 1));
  await page.locator(PAGER).getByRole('button', { name: 'Next' }).click();
  await expect(page.locator(MAIN_ROWS)).toHaveCount(PAGE_SIZE);
  expect(stripUdi(await page.locator(MAIN_UDIS).allTextContents())).toEqual(strong.slice(50, 100).map((r: any) => r.udi));
  await expect(page.locator(PAGER + ' ' + PAGER_INFO)).toHaveText(pageInfo(51, 100, strong.length, pages, 2));
  await page.locator(PAGER).getByRole('button', { name: 'Previous' }).click();
  expect(stripUdi(await page.locator(MAIN_UDIS).allTextContents())).toEqual(strong.slice(0, 50).map((r: any) => r.udi));
  expect(pageErrors).toEqual([]);
});

test('Q2 a filter with fewer than 51 rows has no pager; clearing it brings the pager back on page 1', async ({ page, pageErrors }) => {
  const strong = strongRecords(fixture('devices'));
  const pages = Math.ceil(strong.length / PAGE_SIZE);
  await search(page);
  const next = page.locator(PAGER).getByRole('button', { name: 'Next' });
  for (let i = 0; i < 4; i++) await next.click(); // page 5
  await expect(page.locator(PAGER + ' ' + PAGER_INFO)).toContainText('Page 5 of ' + pages);
  await page.locator('.qfilter').fill(strong[3].udi);
  await expect(page.locator(MAIN_ROWS)).toHaveCount(1);
  await expect(page.locator(PAGER)).toBeHidden();
  await page.locator('.qfilter').fill('');
  await expect(page.locator(PAGER)).toBeVisible();
  await expect(page.locator(PAGER + ' ' + PAGER_INFO)).toHaveText(pageInfo(1, 50, strong.length, pages, 1));
  // A filter that still needs several pages also starts on page 1, not on the old page.
  for (let i = 0; i < 4; i++) await next.click();
  await page.locator('.qfilter').fill('class iii');
  const n = strong.filter((r: any) => r.device_class === 'Class III').length;
  await expect(page.locator('.qcount')).toHaveText(' · ' + n + ' / ' + strong.length + ' rows');
  await expect(page.locator(PAGER + ' ' + PAGER_INFO)).toHaveText(pageInfo(1, 50, n, Math.ceil(n / 50), 1));
  expect(pageErrors).toEqual([]);
});

test('Q3 a chip and a sort return to page 1; the sort covers all rows, not just the visible page', async ({ page, pageErrors }) => {
  const strong = strongRecords(fixture('devices'));
  const pages = Math.ceil(strong.length / PAGE_SIZE);
  await search(page);
  await page.locator(PAGER).getByRole('button', { name: 'Next' }).click(); // page 2
  await page.locator(MAIN_CHIPS).nth(1).click();
  const want = Number(await page.locator(MAIN_CHIPS).nth(1).locator('.cnt').textContent());
  await expect(page.locator(MAIN_ROWS)).toHaveCount(want);
  await expect(page.locator(PAGER)).toBeHidden();
  await page.locator(MAIN_CHIPS).nth(0).click(); // All
  await expect(page.locator(PAGER + ' ' + PAGER_INFO)).toHaveText(pageInfo(1, 50, strong.length, pages, 1));
  await page.locator(PAGER).getByRole('button', { name: 'Next' }).click(); // page 2 again
  // Sort by Device Name ascending: page 1 starts with the first name of ALL rows.
  await page.locator('.dev-card > .dev-table-wrap th[data-col="Device Name"]').click();
  const names = strong.map((r: any) => String(r.device_name).trim()).filter((v: string) => v !== '');
  names.sort((a: string, b: string) => a.localeCompare(b));
  const pageOneMin = strong.slice(0, 50).map((r: any) => String(r.device_name).trim()).filter((v: string) => v !== '')
    .sort((a: string, b: string) => a.localeCompare(b))[0];
  expect(names[0]).not.toBe(pageOneMin); // the check can tell "all rows" from "one page"
  await expect(page.locator(PAGER + ' ' + PAGER_INFO)).toHaveText(pageInfo(1, 50, strong.length, pages, 1));
  await expect(page.locator(MAIN_ROWS).first().locator('td').nth(1)).toHaveText(names[0]);
  expect(pageErrors).toEqual([]);
});

test('Q4 CSV after moving to page 3 holds every filtered row, not the 50 on screen', async ({ page, pageErrors }) => {
  const strong = strongRecords(fixture('devices'));
  await search(page);
  await page.locator('.qfilter').fill('class iii');
  const next = page.locator(PAGER).getByRole('button', { name: 'Next' });
  await next.click();
  await next.click(); // page 3
  const n = strong.filter((r: any) => r.device_class === 'Class III').length;
  await expect(page.locator('.qcount')).toHaveText(' · ' + n + ' / ' + strong.length + ' rows');
  await expect(page.locator(PAGER + ' ' + PAGER_INFO)).toContainText('Page 3 of ' + Math.ceil(n / 50));
  const weakLabel = await page.locator(WEAK_SECTION + ' summary').textContent();
  const wm = /Weak matches \((?:(\d+) \/ )?(\d+)\)/.exec(weakLabel || '')!;
  const weakShown = Number(wm[1] ?? wm[2]);
  const download = page.waitForEvent('download');
  await page.locator('.dev-card button', { hasText: 'CSV' }).click();
  const file = await (await download).path();
  const lines = parseCSV(fs.readFileSync(file!, 'utf8').replace(/^﻿/, ''));
  const body = lines.slice(3).filter((r) => r.length > 1);
  expect(body).toHaveLength(n + weakShown);
  expect(n).toBeGreaterThan(PAGE_SIZE * 2);
  expect(pageErrors).toEqual([]);
});

test('Q5 the weak-matches table paginates on its own', async ({ page, pageErrors }) => {
  const devices = fixture('devices');
  const weak = weakRecords(devices);
  const strong = strongRecords(devices);
  expect(weak.length).toBeGreaterThan(PAGE_SIZE); // the recorded fixture has 68 weak rows
  await search(page);
  await page.locator(WEAK_SECTION + ' summary').click();
  const weakRows = WEAK_SECTION + ' tbody tr';
  await expect(page.locator(weakRows)).toHaveCount(PAGE_SIZE);
  await expect(page.locator(WEAK_PAGER + ' ' + PAGER_INFO)).toHaveText(pageInfo(1, 50, weak.length, 2, 1));
  await page.locator(WEAK_PAGER).getByRole('button', { name: 'Next' }).click();
  await expect(page.locator(weakRows)).toHaveCount(weak.length - PAGE_SIZE);
  await expect(page.locator(WEAK_PAGER + ' ' + PAGER_INFO)).toHaveText(pageInfo(51, weak.length, weak.length, 2, 2));
  // The main table did not move.
  await expect(page.locator(PAGER + ' ' + PAGER_INFO)).toHaveText(pageInfo(1, 50, strong.length, Math.ceil(strong.length / 50), 1));
  expect(stripUdi(await page.locator(MAIN_UDIS).allTextContents())).toEqual(strong.slice(0, 50).map((r: any) => r.udi));
  expect(pageErrors).toEqual([]);
});

test('Q6 the table header stays pinned (opaque, aligned) while the rows scroll under it', async ({ page, pageErrors }) => {
  await search(page);
  const wrap = page.locator('.dev-card > .dev-table-wrap');
  await wrap.scrollIntoViewIfNeeded();
  await wrap.evaluate((el) => { el.scrollTop = 900; });
  const m = await page.evaluate(() => {
    const w = document.querySelector('.dev-card > .dev-table-wrap')!;
    const th = w.querySelector('thead th')!;
    const wr = w.getBoundingClientRect(), tr = th.getBoundingClientRect();
    return {
      scrolled: w.scrollTop, headTop: tr.top, wrapTop: wr.top, vh: window.innerHeight,
      bg: getComputedStyle(th).backgroundColor, z: getComputedStyle(th).zIndex, pos: getComputedStyle(th).position,
    };
  });
  expect(m.scrolled).toBeGreaterThan(300); // the body really is scrolled under the header
  expect(Math.abs(m.headTop - m.wrapTop)).toBeLessThanOrEqual(2);
  expect(m.headTop).toBeGreaterThanOrEqual(0);
  expect(m.headTop).toBeLessThan(m.vh);
  expect(m.pos).toBe('sticky');
  expect(m.bg).not.toMatch(/rgba\(.*,\s*0(\.\d+)?\)$/); // not transparent: rows must not show through
  expect(m.bg).not.toBe('rgba(0, 0, 0, 0)');
  expect(pageErrors).toEqual([]);
});

test('Q7 the Device Records slot keeps its height within the tolerance when it fills', async ({ page, pageErrors }) => {
  const h = held(API_ENDPOINTS);
  await scripted(page, h.handlers);
  await startSearch(page);
  await expect(page.locator(SLOT('devices'))).toHaveAttribute('aria-busy', 'true');
  const height = () => page.locator(SLOT('devices')).evaluate((el) => el.getBoundingClientRect().height);
  const pending = await height();
  h.open(...API_ENDPOINTS);
  await expect(page.locator('#search-btn')).toBeEnabled();
  await page.waitForTimeout(700);
  const filled = await height();
  expect(Math.abs(filled - pending), 'pending ' + Math.round(pending) + ' filled ' + Math.round(filled)).toBeLessThanOrEqual(60);
  expect(pageErrors).toEqual([]);
});

test('Q8 the pagers are labelled navigation landmarks with disabled end buttons and a polite live text', async ({ page, pageErrors }) => {
  const strong = strongRecords(fixture('devices'));
  const pages = Math.ceil(strong.length / PAGE_SIZE);
  await search(page);
  await page.locator(WEAK_SECTION + ' summary').click();
  const nav = page.getByRole('navigation', { name: 'Device records pages' });
  await expect(nav).toHaveCount(1);
  await expect(page.getByRole('navigation', { name: 'Weak matches pages' })).toHaveCount(1);
  const prev = nav.getByRole('button', { name: 'Previous' });
  const next = nav.getByRole('button', { name: 'Next' });
  await expect(prev).toBeDisabled();
  await expect(next).toBeEnabled();
  const live = nav.locator(PAGER_INFO);
  await expect(live).toHaveAttribute('aria-live', 'polite');
  await expect(live).toHaveText(pageInfo(1, 50, strong.length, pages, 1));
  await next.click();
  await expect(live).toHaveText(pageInfo(51, 100, strong.length, pages, 2));
  expect(await page.evaluate(() => (document.activeElement as HTMLElement).textContent)).toBe('Next'); // focus stays on the pressed button
  await expect(prev).toBeEnabled();
  for (let i = 2; i < pages; i++) await next.click();
  await expect(live).toHaveText(pageInfo((pages - 1) * 50 + 1, strong.length, strong.length, pages, pages));
  await expect(next).toBeDisabled();
  await expect(prev).toBeEnabled();
  expect(pageErrors).toEqual([]);
});
