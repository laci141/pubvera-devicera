// No-inline-styles test (CSP style-src S0): web/index.html carries no style
// attribute, sets no inline style from JS, and every utility class that
// replaced an inline style is defined once as a doubled `.u-NAME.u-NAME` rule.
//
// Run: node tools/no_inline_styles_test.js
//
// Source test: the managed list is closed; a doubled .u-* rule that is not on
// the list, or a managed class that is missing, unused or defined twice, fails.
"use strict";
var fs = require("fs");
var path = require("path");

var MANAGED = ["u-inline"];

var file = path.join(__dirname, "..", "web", "index.html");
if (!fs.existsSync(file)) {
  console.error("FAIL: web/index.html not found - refusing to pass silently");
  process.exit(1);
}
var html = fs.readFileSync(file, "utf8").replace(/\r\n/g, "\n");
var styleOpen = /<style\b[^>]*>/.exec(html);
var a = styleOpen ? styleOpen.index : -1;
var b = html.indexOf("</style>");
if (a < 0 || b < a) {
  console.error("FAIL: the <style> block not found in web/index.html - refusing to pass silently");
  process.exit(1);
}
var css = html.slice(a + styleOpen[0].length, b).replace(/\/\*[^]*?\*\//g, "");
var markup = html.slice(0, a) + html.slice(b);

var failures = 0;
function check(ok, msg) {
  if (ok) console.log("ok   " + msg);
  else { console.error("FAIL " + msg); failures++; }
}
function lineOf(idx) { return html.slice(0, idx).split("\n").length; }

// (a) no style attribute in markup or JS strings.
var re = /\sstyle\s*=\s*["']/g, m, hits = [];
while ((m = re.exec(html)) !== null) hits.push("line " + lineOf(m.index) + ": " + html.split("\n")[lineOf(m.index) - 1].trim());
check(hits.length === 0, "(a) no style attribute (found " + hits.length + ")" + (hits.length ? "\n     " + hits.join("\n     ") : ""));

// (b) no inline style set from JS.
var setAttr = (html.match(/setAttribute\(\s*["']style["']/g) || []).length;
var cssText = (html.match(/cssText/g) || []).length;
check(setAttr === 0, "(b) no setAttribute('style' (found " + setAttr + ")");
check(cssText === 0, "(b) no cssText (found " + cssText + ")");

// (c) every managed class: defined exactly once as .NAME.NAME, used in markup.
MANAGED.forEach(function (name) {
  var esc = name.replace(/[-]/g, "\\-");
  var defs = (css.match(new RegExp("\\." + esc + "\\." + esc + "(?![\\w-])", "g")) || []).length;
  var anyDefs = (css.match(new RegExp("\\." + esc + "(?![\\w-])", "g")) || []).length;
  check(defs === 1 && anyDefs === 2, "(c) ." + name + "." + name + " defined exactly once, no other ." + name + " selector (doubled " + defs + ", total refs " + anyDefs + ")");
  var used = (markup.match(/class="[^"]*"/g) || []).filter(function (c) {
    return c.slice(7, -1).split(/\s+/).indexOf(name) >= 0;
  }).length;
  check(used >= 1, "(c) ." + name + " used in markup (" + used + ")");
});

// (d) the closed list equals the doubled .u-* rules in the file.
var found = [];
var dre = /\.(u-[\w-]+)\.\1(?![\w-])/g;
while ((m = dre.exec(css)) !== null) if (found.indexOf(m[1]) < 0) found.push(m[1]);
var want = MANAGED.slice().sort().join(",");
check(found.sort().join(",") === want, "(d) doubled .u-* rules [" + found.join(",") + "] equal the managed list [" + want + "]");

if (failures) {
  console.error(failures + " check(s) failed");
  process.exit(1);
}
console.log("PASS: no inline styles; managed classes " + want);
