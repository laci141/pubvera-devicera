// CalVer release workflow test.
//
// Run: node tools/release_workflow_test.js
//
// Every non-pull_request run on main gets a version YYYY.MM.DD.N, stamped into
// the binary, an image tag, the OCI label, a git tag and a GitHub Release. This
// static check reads .github/workflows/deploy.yml and the Dockerfile and
// asserts the pieces are wired together.
//
// The workflow is checked line by line, never with a regex that spans lines:
// one such regex hung with catastrophic backtracking in the Retractis pilot.
"use strict";
const fs = require("fs");
const path = require("path");

const root = path.join(__dirname, "..");
const lines = fs.readFileSync(path.join(root, ".github", "workflows", "deploy.yml"), "utf8").split(/\r?\n/);
const docker = fs.readFileSync(path.join(root, "Dockerfile"), "utf8").split(/\r?\n/);

let failed = 0;
function check(label, ok) {
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label);
}

// The lines of one step, from its "- name:" to the next step, the next job or
// end of file. start is the index of the "- name:" line, -1 when missing.
function step(name) {
  const start = lines.findIndex((l) => l.trim() === "- name: " + name);
  if (start < 0) return { start: -1, lines: [] };
  const out = [lines[start]];
  for (let j = start + 1; j < lines.length && !/^\s*- name:/.test(lines[j]) && !/^  [\w-]+:\s*$/.test(lines[j]); j++) out.push(lines[j]);
  return { start, lines: out };
}
const has = (s, text) => s.lines.some((l) => l.includes(text));
const ifLine = (s) => s.lines.find((l) => /^\s*if:/.test(l)) || "";

// Jobs: a line with exactly two spaces of indent and a key, inside "jobs:".
const jobsAt = lines.findIndex((l) => l === "jobs:");
const jobs = [];
for (let j = jobsAt + 1; jobsAt >= 0 && j < lines.length; j++) {
  const m = lines[j].match(/^  ([\w-]+):\s*$/);
  if (m) jobs.push({ name: m[1], start: j });
}
jobs.forEach((jb, i) => { jb.end = i + 1 < jobs.length ? jobs[i + 1].start : lines.length; });
const jobOf = (idx) => jobs.find((jb) => idx >= jb.start && idx < jb.end) || { name: "" };
// The permission lines of a job: the indented block under its "permissions:".
function perms(jb) {
  const p = lines.slice(jb.start, jb.end).findIndex((l) => l.trim() === "permissions:");
  if (p < 0) return [];
  const out = [];
  for (let j = jb.start + p + 1; j < jb.end && /^      \S/.test(lines[j]); j++) out.push(lines[j].trim());
  return out;
}

// Every non-pull_request event on main (push and workflow_dispatch) is
// versioned: a workflow_dispatch run also pushes :latest, so it must not ship
// as "dev".
const NONPR_MAIN = /github\.event_name != 'pull_request' && github\.ref == 'refs\/heads\/main'/;
const nonPRMain = (cond) => cond !== "" && NONPR_MAIN.test(cond) && !/event_name\s*==\s*'push'/.test(cond);

const push = step("Build and push");
check("Build and push step exists", push.start >= 0);
const pubJob = jobOf(push.start);

const ver = step("Compute version");
check("version step exists", ver.start >= 0);
check("version step is in the publishing job (" + pubJob.name + ")", ver.start >= 0 && jobOf(ver.start).name === pubJob.name);
check("version step comes after Checkout", ver.start >= 0 && lines.slice(pubJob.start, ver.start).some((l) => l.trim() === "- name: Checkout"));
check("version step if: is non-PR main and not event_name == 'push'", nonPRMain(ifLine(ver)));
check("version step uses date -u +%Y.%m.%d", has(ver, "date -u +%Y.%m.%d"));
check("version step uses git ls-remote --tags", has(ver, "git ls-remote --tags"));
check("version step writes GITHUB_OUTPUT", has(ver, "GITHUB_OUTPUT"));

const argIdx = push.lines.findIndex((l) => /^\s*build-args:/.test(l));
check("pushing build has build-args with APP_VERSION",
  argIdx >= 0 && push.lines.slice(argIdx).some((l) => l.includes("APP_VERSION=")));
const tagLine = push.lines.find((l) => l.includes("steps.version.outputs.version") && l.includes("format(")) || "";
check("version image tag only on non-PR main and not event_name == 'push'", nonPRMain(tagLine));
check("pushing build keeps :latest and :sha tags",
  has(push, "${{ env.IMAGE_NAME }}:latest") && has(push, "${{ env.IMAGE_NAME }}:${{ github.sha }}"));

// :latest is what Watchtower deploys. workflow_dispatch lets the caller pick
// the branch, so the login and the push need the main ref too, not only the
// pull_request exclusion.
const login = step("Log in to GHCR");
check("Log in to GHCR if: is non-PR main", nonPRMain(ifLine(login)));
const pushFlag = push.lines.find((l) => /^\s*push:/.test(l)) || "";
check("Build and push push: is non-PR main", nonPRMain(pushFlag));

const check2 = step("Build image for the non-root check");
check("load: true non-root build still exists", has(check2, "load: true"));
check("non-root build passes APP_VERSION build-arg", has(check2, "APP_VERSION="));
check("non-root build carries no version image tag", !has(check2, "format(") && !has(check2, "steps.version.outputs.version }}:"));
check("non-root build comes before Build and push", check2.start >= 0 && check2.start < push.start);

const rel = step("Create GitHub release");
check("release step exists", rel.start >= 0);
check("release step is in the publishing job", rel.start >= 0 && jobOf(rel.start).name === pubJob.name);
check("release step if: is non-PR main and not event_name == 'push'", nonPRMain(ifLine(rel)));
check("release step runs gh release create --target",
  rel.lines.some((l) => l.includes("gh release create") && l.includes("--target")));
check("release step comes after Build and push", push.start >= 0 && rel.start > push.start);
check("release step uses GH_TOKEN: ${{ github.token }}", has(rel, "GH_TOKEN: ${{ github.token }}"));

for (const jb of jobs) {
  const p = perms(jb);
  if (jb.name === pubJob.name) {
    check("job " + jb.name + " has contents: write", p.includes("contents: write"));
    check("job " + jb.name + " keeps packages: write", p.includes("packages: write"));
  } else {
    check("job " + jb.name + " has no contents: write", !p.includes("contents: write"));
  }
}
check("no workflow-level contents: write", !lines.some((l) => /^  contents:\s*write/.test(l)));
const conc = lines.findIndex((l) => l.trim() === "concurrency:");
check("concurrency group exists", conc >= 0 && (lines[conc + 1] || "").trim().startsWith("group:"));
check("cancel-in-progress is not true", !lines.some((l) => /^\s*cancel-in-progress:\s*true/.test(l)));

check("Dockerfile has ARG APP_VERSION=dev twice", docker.filter((l) => l.trim() === "ARG APP_VERSION=dev").length === 2);
check("Dockerfile ldflags set -X ....appVersion=", docker.some((l) => l.includes("-X ") && l.includes(".appVersion=")));
check("Dockerfile has org.opencontainers.image.version", docker.some((l) => l.includes("org.opencontainers.image.version")));

if (failed) {
  console.error(failed + " check(s) failed");
  process.exit(1);
}
console.log("all release workflow checks pass");
