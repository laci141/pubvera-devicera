'use strict';
// Loads the REAL toCSV() source out of web/index.html and checks the CSV
// formula-injection guard. Plain node, no dependencies.
const fs = require('fs');
const path = require('path');

const html = fs.readFileSync(path.join(__dirname, '..', 'web', 'index.html'), 'utf8');
const start = html.indexOf('function toCSV(');
if (start < 0) { console.log('FAIL: toCSV not found in web/index.html'); process.exit(1); }
const endMatch = /\r?\n  \}\r?\n/.exec(html.slice(start));
if (!endMatch) { console.log('FAIL: end of toCSV not found'); process.exit(1); }
const src = html.slice(start, start + endMatch.index + endMatch[0].length);
const toCSV = new Function(src + '\nreturn toCSV;')();

let failed = 0;
function check(name, ok, detail) {
  if (ok) console.log('PASS ' + name);
  else { failed++; console.log('FAIL ' + name + (detail ? ' :: ' + detail : '')); }
}

// Minimal RFC 4180 parser.
function parse(text) {
  const rows = []; let row = [], f = '', q = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (q) {
      if (c === '"') { if (text[i + 1] === '"') { f += '"'; i++; } else q = false; }
      else f += c;
    } else if (c === '"') q = true;
    else if (c === ',') { row.push(f); f = ''; }
    else if (c === '\r' && text[i + 1] === '\n') { row.push(f); rows.push(row); row = []; f = ''; i++; }
    else f += c;
  }
  row.push(f); rows.push(row);
  return rows;
}

// Cell value as a spreadsheet would see it: parse a one-row, one-column CSV.
function firstData(value) {
  const rows = parse(toCSV([{ A: value }]).replace(/^﻿/, ''));
  return rows[2][0]; // rows[0]=sep=, rows[1]=header, rows[2]=data
}

// a) formula strings are neutralized
[
  '=HYPERLINK("http://x","y")', '+1+1', '-2+3', '@SUM(A1)',
  ' =1+1', '\t=1+1', '\r=1+1'
].forEach(function (s) {
  const got = firstData(s);
  check('a) neutralized ' + JSON.stringify(s), got === "'" + s, 'got ' + JSON.stringify(got));
});

// b) title row from a search term
(function () {
  const term = "=cmd|'/c calc'!A1";
  const rows = parse(toCSV([{ A: 'x' }], term).replace(/^﻿/, ''));
  check('b) title row neutralized', rows[1][0] === "'" + term, 'got ' + JSON.stringify(rows[1][0]));
})();

// c) real numbers unchanged
[-5, 3.14, 0, -0.5].forEach(function (n) {
  const got = firstData(n);
  check('c) number unchanged ' + n, got === String(n), 'got ' + JSON.stringify(got));
});

// d) normal text unchanged
['Medtronic', 'pacemaker lead'].forEach(function (s) {
  check('d) text unchanged ' + s, firstData(s) === s, 'got ' + JSON.stringify(firstData(s)));
});

// e) RFC 4180 escaping
['a"b', 'a,b', 'a\nb'].forEach(function (s) {
  const raw = toCSV([{ A: s }]);
  const expect = '"' + s.replace(/"/g, '""') + '"';
  check('e) quoted ' + JSON.stringify(s), raw.endsWith('\r\n' + expect), 'tail ' + JSON.stringify(raw.slice(-12)));
  check('e) round-trip ' + JSON.stringify(s), firstData(s) === s);
});

// f) BOM, sep=, CRLF, shape
(function () {
  const rows = [{ A: 'x', B: 1 }, { A: 'y', B: 2 }, { A: 'z', B: 3 }];
  const out = toCSV(rows);
  check('f) BOM present', out.charCodeAt(0) === 0xFEFF);
  check('f) sep=, line present', out.slice(1).startsWith('sep=,\r\n'));
  check('f) CRLF only line endings', !/(^|[^\r])\n/.test(out.replace(/"[^"]*"/g, '')));
  const p = parse(out.slice(1));
  check('f) row count (sep + header + 3)', p.length === 5, 'got ' + p.length);
  check('f) column count', p[1].length === 2 && p[2].length === 2);
})();

// g) round-trip column counts with nasty values
(function () {
  const rows = [
    { A: '=1+1', B: 'a,b', C: 'a"b', D: 'a\nb' },
    { A: '\t=x', B: '-5', C: 3.14, D: null },
    { A: '@SUM(A1)', B: ' +1', C: true, D: '' }
  ];
  const p = parse(toCSV(rows, "=cmd|'/c calc'!A1").replace(/^﻿/, ''));
  // p[0]=sep=, p[1]=title (1 col), p[2]=header, rest data
  const body = p.slice(2);
  check('g) every row has same column count', body.every(function (r) { return r.length === 4; }),
    JSON.stringify(body.map(function (r) { return r.length; })));
})();

// h) strictly numeric string stays unchanged
check('h) numeric string "-5" unchanged', firstData('-5') === '-5', 'got ' + JSON.stringify(firstData('-5')));

process.exit(failed ? 1 : 0);
