'use strict';
// Loads the REAL functions out of web/index.html (no copies) and checks the
// 429 message and that the in-flight search guard stays removed. Plain node, no dependencies.
const fs = require('fs');
const path = require('path');

const html = fs.readFileSync(path.join(__dirname, '..', 'web', 'index.html'), 'utf8');

// Source text of "function <name>(" up to its closing "  }" line (2-space indent).
function extract(name) {
  const start = html.indexOf('function ' + name + '(');
  if (start < 0) return null;
  const endMatch = /\r?\n  \}\r?\n/.exec(html.slice(start));
  if (!endMatch) return null;
  return html.slice(start, start + endMatch.index + endMatch[0].length);
}

let failed = 0;
function check(name, ok, detail) {
  if (ok) console.log('PASS ' + name);
  else { failed++; console.log('FAIL ' + name + (detail ? ' :: ' + detail : '')); }
}

const unavailSrc = extract('unavailableText');
const retrySrc = extract('retryAfterSeconds');
const fetchSrc = extract('fetchJson');

// unavailableText(resp) -> string; undefined when the function does not exist yet.
const unavailableText = unavailSrc
  ? new Function((retrySrc || '') + '\n' + unavailSrc + '\nreturn unavailableText;')()
  : function () { return undefined; };

const GENERIC = 'This section is temporarily unavailable.';
const LIMIT = 'Too many requests — please wait a few seconds and try again.';

// a) 429 -> rate-limit message
check('a) 429 gives the rate-limit message',
  unavailableText({ ok: false, status: 429, data: {} }) === LIMIT,
  'got ' + JSON.stringify(unavailableText({ ok: false, status: 429, data: {} })));

// b) other failures keep the generic message
[500, 502, 503, 0].forEach(function (s) {
  const got = unavailableText({ ok: false, status: s, data: {} });
  check('b) status ' + s + ' keeps the generic message', got === GENERIC, 'got ' + JSON.stringify(got));
});
check('b) missing response keeps the generic message', unavailableText(null) === GENERIC,
  'got ' + JSON.stringify(unavailableText(null)));

// c) Retry-After flows from the real fetchJson headers into the message
function runFetchJson(status, headers) {
  if (!fetchSrc) return Promise.resolve(null);
  const fakeFetch = function () {
    return Promise.resolve({
      ok: status >= 200 && status < 300, status: status,
      headers: { get: function (k) { return headers[k] === undefined ? null : headers[k]; } },
      json: function () { return Promise.reject(new Error('not json')); } // Cloudflare serves HTML
    });
  };
  const authHeaders = function () { return Promise.resolve({}); };
  return new Function('authHeaders', 'fetch', fetchSrc + '\nreturn fetchJson;')(authHeaders, fakeFetch)('/api/x');
}

(async function () {
  const r7 = await runFetchJson(429, { 'Retry-After': '7' });
  const t7 = r7 ? unavailableText(r7) : undefined;
  check('c) Retry-After 7 gives "please wait 7 seconds and try again."',
    typeof t7 === 'string' && /please wait 7 seconds and try again\.$/.test(t7), 'got ' + JSON.stringify(t7));

  const rBad = await runFetchJson(429, { 'Retry-After': 'Wed, 21 Oct 2026 07:28:00 GMT' });
  check('c) non-numeric Retry-After falls back to the plain 429 message',
    !!rBad && unavailableText(rBad) === LIMIT, 'got ' + JSON.stringify(rBad && unavailableText(rBad)));

  const r500 = await runFetchJson(500, { 'Retry-After': '7' });
  check('c) Retry-After on a 500 does not change the generic message',
    !!r500 && unavailableText(r500) === GENERIC, 'got ' + JSON.stringify(r500 && unavailableText(r500)));

  // f) the in-flight search guard is gone (a same-term re-search must start)
  ['tryBeginSearch', 'endSearch', 'searchState'].forEach(function (word) {
    check('f) web/index.html does not contain ' + word, html.indexOf(word) < 0,
      'found ' + word + ' at offset ' + html.indexOf(word));
  });

  process.exit(failed ? 1 : 0);
})();
