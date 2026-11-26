// Dockerfile apt upgrade test.
//
// Run: node tools/dockerfile_apt_upgrade_test.js [path/to/Dockerfile]
//
// The runtime stage must upgrade the Debian packages with security fixes
// (zlib1g, libc6/libc-bin, libzstd1, login.defs, passwd) by name with
// --only-upgrade, in the same RUN as the ca-certificates install, and never
// with a bare apt-get upgrade / dist-upgrade / full-upgrade.
//
// This source holds no backslash: CR, LF and the line-continuation character
// are built with String.fromCharCode, and no RegExp is used.
"use strict";
const fs = require("fs");
const path = require("path");

const CR = String.fromCharCode(13);
const LF = String.fromCharCode(10);
const CONT = String.fromCharCode(92);

const file = process.argv[2] || path.join(__dirname, "..", "Dockerfile");
let text = fs.readFileSync(file, "utf8");
if (text.charCodeAt(0) === 0xfeff) text = text.slice(1);
text = text.split(CR + LF).join(LF);
const lines = text.split(LF);

let failed = 0;
function check(label, ok) {
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label);
}

// Final stage: everything after the last FROM line.
let lastFrom = -1;
lines.forEach((l, i) => {
  if (l.trim().toUpperCase().startsWith("FROM ")) lastFrom = i;
});
check("Dockerfile has a FROM line", lastFrom >= 0);
const stage = lines.slice(lastFrom + 1);

// Logical lines: comments skipped, continuation lines joined.
const logical = [];
let buf = "";
for (const raw of stage) {
  const t = raw.trim();
  if (t.startsWith("#")) continue;
  if (t.endsWith(CONT)) {
    buf += t.slice(0, -1).trim() + " ";
    continue;
  }
  buf += t;
  if (buf.trim() !== "") logical.push(buf.trim());
  buf = "";
}
if (buf.trim() !== "") logical.push(buf.trim());

const runIdx = logical.findIndex((l) => l.startsWith("RUN ") && l.includes("apt-get install"));
const userIdx = logical.findIndex((l) => l.startsWith("USER "));
const run = runIdx >= 0 ? logical[runIdx] : "";

check("final stage has a RUN with apt-get install", runIdx >= 0);
for (const needle of [
  "--only-upgrade",
  "--no-install-recommends",
  "ca-certificates",
  "libc6",
  "libc-bin",
  "zlib1g",
  "libzstd1",
  "login.defs",
  "passwd",
  "rm -rf /var/lib/apt/lists/*",
]) {
  check("apt-get RUN contains " + needle, run.includes(needle));
}
for (const bad of ["apt-get upgrade", "apt-get dist-upgrade", "full-upgrade", "apt upgrade"]) {
  check("no logical line contains " + bad, !logical.some((l) => l.includes(bad)));
}
check("final stage has a USER line", userIdx >= 0);
check("apt-get RUN comes before USER", runIdx >= 0 && userIdx >= 0 && runIdx < userIdx);

if (failed) {
  console.log(failed + " check(s) failed");
  process.exit(1);
}
console.log("all checks passed");
