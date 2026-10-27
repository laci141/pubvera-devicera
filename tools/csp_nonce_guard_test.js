// CSP guard test (phase 1).
//
// Run: node tools/csp_nonce_guard_test.js
//
// Unlike Corpova's no_inline_handlers_test.js (which scans the whole file
// without stripping script bodies), this guard checks markup and script
// strings separately:
//   G1 no on*= attribute inside an HTML tag (script bodies stripped first);
//   G2 no on*=" built inside a JS string literal in any inline script;
//   G3 every <script tag carries nonce="__CSP_NONCE__".
"use strict";
const fs = require("fs");
const path = require("path");

const html = fs.readFileSync(path.join(__dirname, "..", "web", "index.html"), "utf8");

let ran = 0;
let failed = 0;
function check(label, ok) {
  ran++;
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label);
}

const scriptRE = /<script\b([^>]*)>([\s\S]*?)<\/script>/gi;
const scripts = [...html.matchAll(scriptRE)];
check("G0 at least one <script> found (found " + scripts.length + ")", scripts.length > 0);

// G1: markup only, script bodies removed so JS like `x.onload = f` is not markup.
const markup = html.replace(scriptRE, "<script$1></script>");
const tagHandlers = [...markup.matchAll(/<[a-z][^>]*\son[a-z]+\s*=/gi)];
check("G1 no on*= attribute in HTML tags (found " + tagHandlers.length + ")", tagHandlers.length === 0);

// G2: handler attributes assembled inside JS strings.
let strHandlers = 0;
for (const s of scripts) {
  strHandlers += [...s[2].matchAll(/\son[a-z]+\s*=\s*\\?["']/gi)].length;
}
check("G2 no on*=\" built inside script strings (found " + strHandlers + ")", strHandlers === 0);

// G3: every opening <script tag carries the marker.
const opens = [...html.matchAll(/<script\b[^>]*>/gi)].map((m) => m[0]);
const missing = opens.filter((t) => !/\snonce="__CSP_NONCE__"/.test(t));
check("G3 every <script tag has nonce=\"__CSP_NONCE__\" (" + opens.length + " tags, " + missing.length + " missing)", opens.length > 0 && missing.length === 0);
for (const t of missing) console.log("      missing: " + t.slice(0, 80));

if (ran === 0) {
  console.error("FAIL: zero checks ran - refusing to pass silently");
  process.exit(1);
}
console.log((ran - failed) + "/" + ran + " passed, " + failed + " failed");
process.exit(failed ? 1 : 0);
