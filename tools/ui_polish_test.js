// UI polish static test.
//
// Run: node tools/ui_polish_test.js
// Set UI_POLISH_HTML=<path> to check another copy (used for mutation runs).
//
// Reads web/index.html and asserts the UI polish rules: dynamic version badge,
// glass blur standard (15px, 12px under 768px), one body::before background
// instead of orb divs, a 0.7rem font-size floor, brand colour from the theme,
// a :focus-visible outline, a labelled search input, HTML year labels for the
// trend chart, a non-scaling sparkline stroke, and no inline handlers/styles.
"use strict";
const fs = require("fs");
const path = require("path");

const file = process.env.UI_POLISH_HTML || path.join(__dirname, "..", "web", "index.html");
const html = fs.readFileSync(file, "utf8").replace(/\r/g, "");

let failed = 0;
function check(label, ok) {
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label);
}

const styleM = html.match(/<style\b[^>]*>([\s\S]*?)<\/style>/i);
const css = styleM ? styleM[1] : "";
check("0 a <style> block exists", css !== "");

check("1 no hardcoded v0.1.0", !html.includes("v0.1.0"));
check("1 id=\"hdr-version\" present", /\bid="hdr-version"/.test(html));

const b15 = (css.match(/blur\(15px\)/g) || []).length;
check("2 blur(15px) count >= 4 (found " + b15 + ")", b15 >= 4);
const m768 = css.match(/@media \(max-width: 768px\) \{\s*\.glass, \.brand-bar \{([^}]*)\}/);
check("2 768px media block sets blur(12px) for both properties",
  !!m768 && /-webkit-backdrop-filter:\s*blur\(12px\)/.test(m768[1]) && /(^|[^-])backdrop-filter:\s*blur\(12px\)/.test(m768[1]));
const before = css.match(/body::before\s*\{([^}]*)\}/);
check("2 body::before with radial-gradient", !!before && /radial-gradient/.test(before[1]));
check("2 no class=\"orb in the HTML", !/class="orb/.test(html));

const small = [];
for (const m of css.matchAll(/font-size:\s*([0-9.]+)rem/g)) {
  if (parseFloat(m[1]) < 0.7) small.push(m[0]);
}
check("4 no CSS font-size below 0.7rem (found " + small.join(", ") + ")", small.length === 0);

check("5 .brand-mark uses var(--accent)", /\.brand-mark\s*\{[^}]*color:\s*var\(--accent\)/.test(css));
check("5 no #60a5fa in brand rules", !/\.brand-(mark|link)[^{]*\{[^}]*#60a5fa/.test(css));

check("7 a :focus-visible rule with outline", /:where\(button, a, input, summary, \[role="menuitem"\]\):focus-visible\s*\{[^}]*outline:/.test(css));

check("9 <label for=\"device-input\"", /<label for="device-input"/.test(html));

check("3 no <text> created in SVG", !/createElementNS\(\s*SVG_NS\s*,\s*['"]text['"]\s*\)/.test(html));
check("3 polyline has non-scaling-stroke", /non-scaling-stroke/.test(html));

const handlers = (html.match(/ on[a-z]*=/g) || []).length;
check("11 inline handler count 0 (found " + handlers + ")", handlers === 0);
const styles = (html.match(/style="/g) || []).length;
check("11 style=\" count 0 (found " + styles + ")", styles === 0);

if (failed) {
  console.log(failed + " check(s) failed");
  process.exit(1);
}
console.log("all checks passed");
