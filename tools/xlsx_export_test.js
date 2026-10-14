'use strict';
// Loads the REAL XLSX export helpers out of web/index.html and checks the
// workbook they build with xlsx-js-style 1.2.0 (the e2e devDependency, the
// version the page pins): two sheets, autofilter, real date cells, formula
// guard, and whether the library writes a freeze <pane> at all.
// A far-from-UTC zone, so a local-time date computation shows up.
process.env.TZ = 'Pacific/Kiritimati';
const fs = require('fs');
const path = require('path');
const XLSX = require(path.join(__dirname, '..', 'e2e', 'node_modules', 'xlsx-js-style'));

const html = fs.readFileSync(path.join(__dirname, '..', 'web', 'index.html'), 'utf8');
function extract(name) {
  const start = html.indexOf('function ' + name + '(');
  if (start < 0) { console.log('FAIL: ' + name + ' not found in web/index.html'); process.exit(1); }
  const endMatch = /\r?\n  \}\r?\n/.exec(html.slice(start));
  if (!endMatch) { console.log('FAIL: end of ' + name + ' not found'); process.exit(1); }
  return html.slice(start, start + endMatch.index + endMatch[0].length);
}
const names = ['xlsxDateSerial', 'xlsxSafeText', 'buildXLSXSheet', 'buildXLSXWorkbook'];
const lib = new Function('XLSX', names.map(extract).join('\n') + '\nreturn {' + names.join(',') + '};')(XLSX);

let failed = 0;
function check(name, ok, detail) {
  if (ok) console.log('PASS ' + name);
  else { failed++; console.log('FAIL ' + name + (detail ? ' :: ' + detail : '')); }
}

function sheetXML(wb, n) {
  const buf = XLSX.write(wb, { type: 'buffer', bookType: 'xlsx' });
  const cfb = XLSX.CFB.read(buf, { type: 'buffer' });
  const i = cfb.FullPaths.findIndex(function (p) { return p.endsWith('/xl/worksheets/sheet' + n + '.xml'); });
  return i >= 0 ? Buffer.from(cfb.FileIndex[i].content).toString('utf8') : '';
}

// 0) control: the zone is really far from UTC (else the local-time check is void)
check('0) TZ offset is not zero', new Date(2024, 2, 20).getTimezoneOffset() !== 0,
  'offset ' + new Date(2024, 2, 20).getTimezoneOffset());

// 1) freeze proof: does xlsx-js-style 1.2.0 write a <pane> for either form?
function freezeXML(set) {
  const ws = XLSX.utils.aoa_to_sheet([['t'], ['h'], [1], [2]]);
  set(ws);
  const wb = XLSX.utils.book_new();
  XLSX.utils.book_append_sheet(wb, ws, 'S');
  return sheetXML(wb, 1);
}
const paneFreeze = /<pane[\s>]/.test(freezeXML(function (ws) { ws['!freeze'] = { xSplit: 0, ySplit: 2 }; }));
const paneViews = /<pane[\s>]/.test(freezeXML(function (ws) { ws['!views'] = [{ state: 'frozen', ySplit: 2 }]; }));
console.log('INFO freeze proof: !freeze emits <pane: ' + paneFreeze + '; !views emits <pane: ' + paneViews);

// 2) the real workbook
const rows1 = [
  { UDI: 'D1', 'Device Name': '=1+1', Company: '+1', 'Last Update': '2024-03-20', 'First Published': '20240320', 'Model': '-5' },
  { UDI: 'D2', 'Device Name': '-1+1', Company: '@SUM(A1)', 'Last Update': '2024-13-45', 'First Published': 'n/a', 'Model': 3.5 },
  { UDI: 'D3', 'Device Name': ' =1', Company: 'Medtronic', 'Last Update': '', 'First Published': '2024-02-30', 'Model': '' }
];
const rows2 = rows1.map(function (r, i) { const o = Object.assign({}, r); o.Group = i + 1; return o; });
const wb = lib.buildXLSXWorkbook([
  { name: 'Devicera', rows: rows1, title: 'Devicera export · query: "x"' },
  { name: 'All records', rows: rows2, title: '=title' }
]);
check('2) two sheets', JSON.stringify(wb.SheetNames) === '["Devicera","All records"]', JSON.stringify(wb.SheetNames));

wb.SheetNames.forEach(function (name, si) {
  const ws = wb.Sheets[name];
  const range = XLSX.utils.decode_range(ws['!ref']);
  const want = XLSX.utils.encode_range({ s: { r: 1, c: 0 }, e: { r: range.e.r, c: range.e.c } });
  check('3) ' + name + ' !autofilter.ref = ' + want, ws['!autofilter'] && ws['!autofilter'].ref === want,
    JSON.stringify(ws['!autofilter']));
  const xml = sheetXML(wb, si + 1);
  check('3) ' + name + ' <autoFilter> written', xml.indexOf('<autoFilter ref="' + want + '"') !== -1);

  const head = {};
  for (let c = range.s.c; c <= range.e.c; c++) head[ws[XLSX.utils.encode_cell({ r: 1, c: c })].v] = c;
  const at = function (r, h) { return ws[XLSX.utils.encode_cell({ r: r, c: head[h] })]; };
  const d = at(2, 'Last Update');
  check('4) ' + name + ' 2024-03-20 is a date serial', d && d.t === 'n' && d.v === 45371 && d.z === 'yyyy-mm-dd',
    JSON.stringify(d));
  const d8 = at(2, 'First Published');
  check('4) ' + name + ' 20240320 is a date serial', d8 && d8.t === 'n' && d8.v === 45371 && d8.z === 'yyyy-mm-dd',
    JSON.stringify(d8));
  [[3, 'Last Update', '2024-13-45'], [3, 'First Published', 'n/a'], [4, 'First Published', '2024-02-30']].forEach(function (x) {
    const cell = at(x[0], x[1]);
    check('4) ' + name + ' invalid date ' + x[2] + ' stays text', cell && cell.t === 's' && cell.v === x[2] && !cell.z,
      JSON.stringify(cell));
  });

  [[2, 'Device Name', '=1+1'], [2, 'Company', '+1'], [3, 'Device Name', '-1+1'], [3, 'Company', '@SUM(A1)'], [4, 'Device Name', ' =1']]
    .forEach(function (x) {
      const cell = at(x[0], x[1]);
      check('5) ' + name + ' neutralized ' + JSON.stringify(x[2]), cell && cell.t === 's' && cell.v === "'" + x[2],
        JSON.stringify(cell));
    });
  check('5) ' + name + ' numeric string "-5" unchanged', at(2, 'Model').v === '-5', JSON.stringify(at(2, 'Model')));
  check('5) ' + name + ' number 3.5 unchanged', at(3, 'Model').t === 'n' && at(3, 'Model').v === 3.5);
  let risky = 0;
  Object.keys(ws).forEach(function (a) {
    if (a[0] === '!') return;
    const c = ws[a];
    if (c.f) risky++;
    if (c.t === 's' && /^[ \t\r\n]*[=+\-@]/.test(c.v) && !/^-?\d+(\.\d+)?$/.test(c.v)) risky++;
  });
  check('5) ' + name + ' 0 risky text cells, 0 formulas', risky === 0, risky + ' risky');
  check('6) ' + name + ' column widths set', Array.isArray(ws['!cols']) && ws['!cols'].length === range.e.c + 1 &&
    ws['!cols'].every(function (c) { return c.wch >= 9; }));
  check('6) ' + name + ' freeze matches the proof', /<pane[\s>]/.test(xml) === (paneFreeze || paneViews));
});
check('2) Group column only on All records',
  !('Group' in rowsHeader(wb.Sheets['Devicera'])) && ('Group' in rowsHeader(wb.Sheets['All records'])));
function rowsHeader(ws) {
  const out = {}; const range = XLSX.utils.decode_range(ws['!ref']);
  for (let c = 0; c <= range.e.c; c++) out[ws[XLSX.utils.encode_cell({ r: 1, c: c })].v] = c;
  return out;
}

// 7) read-back: the numeric serial and its date format survive the file
(function () {
  const back = XLSX.read(XLSX.write(wb, { type: 'buffer', bookType: 'xlsx' }), { type: 'buffer', cellNF: true });
  const ws = back.Sheets['All records'];
  const c = rowsHeader(ws)['Last Update'];
  const cell = ws[XLSX.utils.encode_cell({ r: 2, c: c })];
  check('7) read-back date cell numeric with yyyy-mm-dd', cell && cell.t === 'n' && cell.v === 45371 && /yyyy.?-?mm.?-?dd/.test(cell.z || ''),
    JSON.stringify(cell));
})();

// 8) control: dead freeze line is gone when the library ignores it
if (!paneFreeze && !paneViews) {
  check("8) no dead ws['!freeze'] in web/index.html", html.indexOf("ws['!freeze']") === -1);
}

process.exit(failed ? 1 : 0);
