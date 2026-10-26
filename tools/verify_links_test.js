// Verify-at-source link test.
//
// Run: node tools/verify_links_test.js
//
// verifyLinks(type, device) puts the typed device name inside a quoted openFDA
// phrase (%22...%22). A double quote in the name would close the phrase early
// and a backslash would escape the closing quote, so both are removed first,
// the same rule as cliutil.Phrase on the server. This test extracts the real
// function from web/index.html and checks the generated URLs.
"use strict";
const fs = require("fs");
const path = require("path");

const html = fs.readFileSync(path.join(__dirname, "..", "web", "index.html"), "utf8").replace(/\r/g, "");
const start = html.indexOf("  function verifyLinks(");
const end = html.indexOf("  function fmtNum(");
if (start < 0 || end < start) {
  console.log("FAIL  could not extract verifyLinks from web/index.html");
  process.exit(1);
}
const verifyLinks = new Function(html.slice(start, end) + "\nreturn verifyLinks;")();

let failed = 0;
function check(label, ok) {
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label);
}

function phraseOf(url) {
  const m = url.match(/%22(.*?)%22/);
  return m ? m[1] : null;
}

const plain = verifyLinks("CORROBORATION", "pacemaker")[0].url;
check("1 plain name unchanged", plain.includes("generic_name:%22pacemaker%22&count=event_type.exact"));

const spaced = verifyLinks("CORROBORATION", "insulin pump")[0].url;
check("2 space stays encoded as %20", phraseOf(spaced) === "insulin%20pump");

const quoted = verifyLinks("CORROBORATION", 'a"b')[0].url;
check("3 double quote removed, exactly two %22 in the URL", phraseOf(quoted) === "ab" && (quoted.match(/%22/g) || []).length === 2);

const slashed = verifyLinks("CORROBORATION", "abc\\")[0].url;
check("4 trailing backslash removed (no %5C)", phraseOf(slashed) === "abc" && !slashed.includes("%5C"));

const both = verifyLinks("CORROBORATION", 'a\\"b')[0].url;
check("5 backslash + quote removed", phraseOf(both) === "ab" && !both.includes("%5C"));

const all = verifyLinks("CORROBORATION", 'x"y\\')
  .map((l) => l.url)
  .filter((u) => u.includes("%22") || u.includes("term="));
check("6 no link carries %22 inside the term or %5C", all.every((u) => !u.includes("%5C") && (u.match(/%22/g) || []).length <= 2));

if (failed) {
  console.log(failed + " check(s) failed");
  process.exit(1);
}
console.log("all verify link checks pass");
