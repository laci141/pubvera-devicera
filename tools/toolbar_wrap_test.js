// Toolbar wrap test (W1-W6): the Device Records toolbar and the category chips
// wrap instead of pushing the page sideways on a phone.
//
// Run: node tools/toolbar_wrap_test.js
//
// CSS-source test: comments are stripped, the @media blocks are split off by
// brace matching, and the top-level rules are read by selector. This is NOT a
// layout test; the real widths are measured with Playwright.
"use strict";
var fs = require("fs");
var path = require("path");

var html = fs.readFileSync(path.join(__dirname, "..", "web", "index.html"), "utf8").replace(/\r\n/g, "\n");
var a = html.indexOf("<style>");
var b = html.indexOf("</style>");
if (a < 0 || b < a) {
  console.error("FAIL: the <style> block not found in web/index.html - refusing to pass silently");
  process.exit(1);
}
var css = html.slice(a + 7, b).replace(/\/\*[^]*?\*\//g, "");

// Split into top-level text and @media blocks by brace matching.
var top = "";
var media = [];
var i = 0;
while (i < css.length) {
  var at = css.indexOf("@media", i);
  if (at < 0) { top += css.slice(i); break; }
  top += css.slice(i, at);
  var open = css.indexOf("{", at);
  var depth = 0, j = open;
  for (; j < css.length; j++) {
    if (css[j] === "{") depth++;
    else if (css[j] === "}" && --depth === 0) break;
  }
  media.push({ query: css.slice(at + 6, open).replace(/\s+/g, ""), body: css.slice(open + 1, j) });
  i = j + 1;
}
function rulesOf(src) {
  var out = [];
  src.replace(/([^{}]+)\{([^{}]*)\}/g, function (_, sel, body) {
    out.push({ sel: sel.split(",").map(function (s) { return s.trim().replace(/\s+/g, " "); }), body: body.replace(/\s+/g, "") });
  });
  return out;
}
function decls(src, sel) {
  return ";" + rulesOf(src).filter(function (r) { return r.sel.indexOf(sel) >= 0; }).map(function (r) { return r.body; }).join(";");
}
function has(d, p) { return d.indexOf(";" + p) >= 0 || d.indexOf("{" + p) >= 0; }

var ran = 0, failed = 0;
function check(label, ok) {
  ran++;
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label);
}

var row = decls(top, ".meta-row").replace(/;;+/g, ";");
var meta = decls(top, ".meta-row .meta").replace(/;;+/g, ";");
var group = decls(top, ".dl-group").replace(/;;+/g, ";");
var qf = decls(top, ".qfilter").replace(/;;+/g, ";");
var chip = decls(top, ".cat-bar .dl").replace(/;;+/g, ";");
var m480 = media.filter(function (m) { return m.query === "(max-width:480px)"; })[0];
var g480 = m480 ? decls(m480.body, ".dl-group") : "";
var q480 = m480 ? decls(m480.body, ".qfilter") : "";

check("W1 .meta-row display:flex, flex-wrap:wrap, a gap", has(row, "display:flex") && has(row, "flex-wrap:wrap") && /;gap:[^;]+/.test(row));
check("W2 .meta-row .meta and .dl-group min-width:0; .dl-group max-width:100% and no flex-shrink:0",
  has(meta, "min-width:0") && has(group, "min-width:0") && has(group, "max-width:100%") && !has(group, "flex-shrink:0"));
check("W3 .dl-group flex-wrap:wrap", has(group, "flex-wrap:wrap"));
check("W4 .meta-row .meta overflow-wrap:anywhere", has(meta, "overflow-wrap:anywhere"));
check("W5 .qfilter min-width:0 and max-width:100%; @media (max-width: 480px) has .dl-group width:100% and .qfilter flex 1 1 100%",
  has(qf, "min-width:0") && has(qf, "max-width:100%") && has(g480, "width:100%") && has(q480, "flex:11100%"));
check("W6 .cat-bar .dl white-space:normal, max-width:100%, flex-shrink:1, overflow-wrap:anywhere",
  has(chip, "white-space:normal") && has(chip, "max-width:100%") && has(chip, "flex-shrink:1") && has(chip, "overflow-wrap:anywhere"));

if (ran === 0) {
  console.error("FAIL: zero checks ran - refusing to pass silently");
  process.exit(1);
}
console.log((ran - failed) + "/" + ran + " passed, " + failed + " failed");
process.exit(failed ? 1 : 0);
