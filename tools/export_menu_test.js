// Export menu test (E1-E13): the Device Records toolbar has ONE "Export" menu
// button (WAI-ARIA APG menu button pattern) instead of four download buttons.
//
// Run: node tools/export_menu_test.js
//
// Behaviour test without a browser. metaRow() and the block between the
// "// ── Export menu" and "// ── end Export menu" markers are cut out of
// web/index.html by the same `function NAME(` .. "\n  }\n" rule the other
// tools tests use, and run inside node:vm against a tiny fake DOM. The real
// delegated listeners are then driven with fake click and keydown events. The
// download functions are stubs that click a synthetic anchor, like
// triggerDownload() does. Layout is NOT checked here: that is Playwright's job.
"use strict";
var fs = require("fs");
var path = require("path");
var vm = require("vm");

var html = fs.readFileSync(path.join(__dirname, "..", "web", "index.html"), "utf8").replace(/\r\n/g, "\n");

var ran = 0;
var failed = 0;
function check(label, ok) {
  ran++;
  if (!ok) failed++;
  console.log((ok ? "PASS" : "FAIL") + "  " + label);
}
function refuse(what) {
  console.error("FAIL: " + what + " not found in web/index.html - refusing to pass silently");
  process.exit(1);
}
function extract(name) {
  var start = html.indexOf("function " + name + "(");
  if (start < 0) return null;
  var end = html.indexOf("\n  }\n", start);
  if (end < 0) return null;
  return html.slice(start, end + 4);
}

var metaSrc = extract("metaRow");
if (!metaSrc) refuse("function metaRow");
var ma = html.indexOf("// ── Export menu");
var mb = html.indexOf("// ── end Export menu");
if (ma < 0 || mb < ma) refuse("the Export menu marker block");
var blockSrc = html.slice(ma, mb);
var styleStart = html.indexOf("<style>");
var styleEnd = html.indexOf("</style>");
if (styleStart < 0 || styleEnd < styleStart) refuse("the <style> block");
var style = html.slice(styleStart + 7, styleEnd).replace(/\/\*[^]*?\*\//g, "");

// ── tiny DOM: enough for the selectors the toolbar and menu code use ──
class El {
  constructor(tag, attrs) { this.tag = tag; this.attrs = attrs || {}; this.children = []; this.parent = null; }
  getAttribute(n) { return n in this.attrs ? this.attrs[n] : null; }
  setAttribute(n, v) { this.attrs[n] = String(v); }
  removeAttribute(n) { delete this.attrs[n]; }
  hasAttribute(n) { return n in this.attrs; }
  get hidden() { return this.hasAttribute("hidden"); }
  set hidden(v) { if (v) this.attrs.hidden = ""; else delete this.attrs.hidden; }
  get id() { return this.attrs.id || ""; }
  get classList() { var c = (this.attrs.class || "").split(/\s+/); return { contains: function (x) { return c.indexOf(x) >= 0; } }; }
  focus() { doc.activeElement = this; }
  contains(o) { for (; o; o = o.parent) if (o === this) return true; return false; }
  all() { var out = []; var walk = function (e) { e.children.forEach(function (c) { out.push(c); walk(c); }); }; walk(this); return out; }
  matches(sel) { var self = this; return sel.split(",").some(function (s) { return matchChain(self, s.trim().split(/\s+/)); }); }
  closest(sel) { for (var e = this; e; e = e.parent) if (e.matches && e.matches(sel)) return e; return null; }
  querySelectorAll(sel) { return this.all().filter(function (e) { return e.matches(sel); }); }
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
}
function matchSimple(e, s) {
  if (!e || !(e instanceof El)) return false;
  var parts = s.match(/\[[^\]]+\]|\.[\w-]+|^[a-z]+/g) || [];
  return parts.length > 0 && parts.every(function (p) {
    if (p[0] === ".") return e.classList.contains(p.slice(1));
    if (p[0] === "[") {
      var m = p.match(/^\[([\w-]+)(?:="([^"]*)")?\]$/);
      return m[2] === undefined ? e.hasAttribute(m[1]) : e.getAttribute(m[1]) === m[2];
    }
    return e.tag === p;
  });
}
function matchChain(e, parts) {
  if (!matchSimple(e, parts[parts.length - 1])) return false;
  if (parts.length === 1) return true;
  for (var p = e.parent; p; p = p.parent) if (matchChain(p, parts.slice(0, -1))) return true;
  return false;
}
function parse(src, root) {
  var cur = root;
  var re = /<(\/?)(\w+)([^>]*)>/g, m;
  while ((m = re.exec(src)) !== null) {
    if (m[1]) { cur = cur.parent; continue; }
    var attrs = {};
    var are = /([\w-]+)(?:="([^"]*)")?/g, at;
    while ((at = are.exec(m[3])) !== null) attrs[at[1]] = at[2] === undefined ? "" : at[2];
    var el = new El(m[2], attrs);
    el.parent = cur;
    cur.children.push(el);
    if (!/^(input|br|img)$/.test(m[2])) cur = el;
  }
}

var body = new El("body");
var listeners = { click: [], keydown: [] };
var doc = {
  activeElement: body, body: body,
  querySelector: function (s) { return body.querySelector(s); },
  querySelectorAll: function (s) { return body.querySelectorAll(s); },
  getElementById: function (id) { return body.all().find(function (e) { return e.id === id; }) || null; },
  addEventListener: function (t, f) { if (listeners[t]) listeners[t].push(f); },
};
function fire(type, target, trusted, key) {
  var e = { type: type, target: target, key: key, isTrusted: trusted !== false, defaultPrevented: false,
    preventDefault: function () { this.defaultPrevented = true; }, stopPropagation: function () {} };
  listeners[type].forEach(function (f) { f(e); });
  return e;
}
var anchor = new El("a");
anchor.parent = body;
body.children.push(anchor);
var calls = [];
function exporter(fmt) { return function (k, f) { calls.push(fmt + " " + k + " " + f); fire("click", anchor, false); }; }
var sandbox = {
  document: doc, console: console,
  downloadXLSX: exporter("xlsx"), downloadCSV: exporter("csv"), downloadJSON: exporter("json"), downloadBibTeX: exporter("bib"),
};
vm.createContext(sandbox);
function attempt(f) { try { return !!f(); } catch (e) { console.log("      note: " + e.message); return false; } }
attempt(function () { vm.runInContext(blockSrc, sandbox); return true; });
vm.runInContext(metaSrc, sandbox);

var specs = [["udi", "devicera-devices"], ["recalls", "devicera-recalls"]];
var bars = specs.map(function (s) {
  var src = vm.runInContext("metaRow('12 devices', '" + s[0] + "', '" + s[1] + ".json')", sandbox);
  var holder = new El("div");
  holder.parent = body;
  body.children.push(holder);
  parse(src, holder);
  var trigger = holder.querySelector('[aria-haspopup="menu"]');
  var menu = trigger ? doc.getElementById(trigger.getAttribute("aria-controls")) : null;
  var items = menu ? menu.querySelectorAll('[role="menuitem"]') : [];
  return { src: src, key: s[0], base: s[1], holder: holder, trigger: trigger, menu: menu, items: items,
    triggers: holder.querySelectorAll('[aria-haspopup="menu"]') };
});
var A = bars[0];
var allSrc = bars.map(function (x) { return x.src; }).join("\n");
function isOpen(x) { return !!x.menu && !x.menu.hidden && x.trigger.getAttribute("aria-expanded") === "true"; }
function isClosed(x) { return !!x.menu && x.menu.hidden && x.trigger.getAttribute("aria-expanded") === "false"; }
function reset() { bars.forEach(function (x) { if (x.menu) x.menu.hidden = true; if (x.trigger) x.trigger.setAttribute("aria-expanded", "false"); }); doc.activeElement = body; }

// ── markup ──
check("E1 one trigger \"⬇ Export ▾\" with aria-haspopup=menu, aria-expanded=false, aria-controls -> hidden role=menu panel (" +
  bars.map(function (x) { return x.triggers.length; }).join(",") + " triggers per toolbar)",
  bars.every(function (x) {
    return x.triggers.length === 1 && x.trigger.tag === "button" &&
      x.trigger.getAttribute("aria-expanded") === "false" && />⬇ Export ▾<\/button>/.test(x.src) &&
      !!x.menu && x.holder.contains(x.menu) && x.menu.getAttribute("role") === "menu" && x.menu.hidden;
  }));

var want = { Excel: ["xlsx", ".xlsx"], CSV: ["csv", ".csv"], JSON: ["json", ".json"], BibTeX: ["bib", ".bib"] };
check("E2 four role=menuitem buttons Excel, CSV, JSON, BibTeX with data-dl, data-key, data-filename; BibTeX title kept; " +
  "no download button outside the panel; .qfilter present; Class III only for udi",
  bars.every(function (x) {
    var labels = [], re = /role="menuitem"[^>]*>([^<]*)</g, m;
    while ((m = re.exec(x.src)) !== null) labels.push(m[1]);
    var outside = x.holder.querySelectorAll("button").filter(function (b) {
      return !b.closest(".dl-menu") && !b.classList.contains("dl-trigger") && !b.classList.contains("quick-class3");
    }).length;
    return x.items.length === 4 && outside === 0 && labels.join(",") === "Excel,CSV,JSON,BibTeX" &&
      x.items.every(function (it, i) {
        var w = want[labels[i]];
        return it.tag === "button" && it.getAttribute("type") === "button" && it.getAttribute("data-dl") === w[0] &&
          it.getAttribute("data-key") === x.key && it.getAttribute("data-filename") === x.base + w[1];
      }) && !!x.holder.querySelector(".qfilter");
  }) &&
  A.items.length === 4 && A.items[3].getAttribute("title") === "BibTeX citations for these device records (@misc)" &&
  !!A.holder.querySelector(".quick-class3") && !bars[1].holder.querySelector(".quick-class3"));

var onAttr = (allSrc.match(/\son[a-z]+\s*=/gi) || []).length;
var styleAttr = (allSrc.match(/\sstyle\s*=/gi) || []).length;
check("E3 the toolbar markup has no on*= and no style= attribute (" + onAttr + " on*=, " + styleAttr + " style=)",
  onAttr === 0 && styleAttr === 0);

var ids = bars.map(function (x) { return x.menu ? x.menu.id : ""; });
check("E4 toolbars rendered one after another get different panel ids (" + ids.join(", ") + ")",
  ids.every(function (id) { return id !== ""; }) && ids[0] !== ids[1] &&
  bars.every(function (x) { return x.trigger.getAttribute("aria-controls") === x.menu.id; }));

// ── behaviour ──
reset();
check("E5 a trigger click opens: aria-expanded=true, hidden removed, first item focused", attempt(function () {
  fire("click", A.trigger);
  return isOpen(A) && doc.activeElement === A.items[0];
}));

check("E6 ArrowDown/ArrowUp cycle with wrap, Home/End jump to first/last", attempt(function () {
  reset();
  fire("click", A.trigger);
  var last = A.items[A.items.length - 1];
  fire("keydown", doc.activeElement, true, "ArrowDown");
  var d1 = doc.activeElement === A.items[1];
  fire("keydown", doc.activeElement, true, "End");
  var end = doc.activeElement === last;
  fire("keydown", doc.activeElement, true, "ArrowDown");
  var wrapDown = doc.activeElement === A.items[0];
  fire("keydown", doc.activeElement, true, "ArrowUp");
  var wrapUp = doc.activeElement === last;
  fire("keydown", doc.activeElement, true, "Home");
  return d1 && end && wrapDown && wrapUp && doc.activeElement === A.items[0] && isOpen(A);
}));

check("E7 Escape closes (aria-expanded=false, hidden back), returns focus to the trigger, preventDefault", attempt(function () {
  reset();
  fire("click", A.trigger);
  var e = fire("keydown", doc.activeElement, true, "Escape");
  return isClosed(A) && doc.activeElement === A.trigger && e.defaultPrevented;
}));

check("E8 ArrowDown, Enter and Space on the trigger open and focus the first item; Tab closes without preventDefault", attempt(function () {
  var opened = ["ArrowDown", "Enter", " "].map(function (k) {
    reset();
    A.trigger.focus();
    var e = fire("keydown", A.trigger, true, k);
    var ok = isOpen(A) && doc.activeElement === A.items[0] && e.defaultPrevented;
    if (!ok) console.log("      note: key " + JSON.stringify(k) + " did not open the menu");
    return ok;
  });
  reset();
  fire("click", A.trigger);
  var tab = fire("keydown", doc.activeElement, true, "Tab");
  return opened.every(Boolean) && isClosed(A) && !tab.defaultPrevented;
}));

check("E9 an outside trusted click closes; an untrusted click (the download anchor) is ignored", attempt(function () {
  reset();
  fire("click", A.trigger);
  fire("click", anchor, false);
  var keptOpen = isOpen(A);
  fire("click", body, true);
  return keptOpen && isClosed(A);
}));

var got = [];
check("E10 an item click runs its download once with (key, filename), closes the menu, focus back on the trigger", attempt(function () {
  var ok = A.items.length === 4 && A.items.every(function (it) {
    reset();
    fire("click", A.trigger);
    doc.activeElement = body; // WebKit does not focus a button on mouse click
    calls.length = 0;
    fire("click", it, true);
    got.push(calls.join("+"));
    return calls.length === 1 && isClosed(A) && doc.activeElement === A.trigger;
  });
  console.log("      calls: " + (got.join(" | ") || "none"));
  return ok && got.join("|") === "xlsx udi devicera-devices.xlsx|csv udi devicera-devices.csv|json udi devicera-devices.json|bib udi devicera-devices.bib";
}));

check("E11 opening a second menu closes the first", attempt(function () {
  reset();
  var B = bars[1];
  fire("click", A.trigger);
  var first = isOpen(A);
  fire("click", B.trigger);
  return first && isClosed(A) && isOpen(B) && doc.activeElement === B.items[0];
}));
reset();

// ── CSS ──
var used = {};
allSrc.replace(/class="([^"]+)"/g, function (_, c) { c.split(/\s+/).forEach(function (x) { used[x] = 1; }); });
var missing = Object.keys(used).filter(function (c) { return !new RegExp("\\." + c + "(?![\\w-])").test(style); });
check("E12 every class used by the toolbar and menu markup exists in the CSS (missing: " + (missing.join(",") || "none") + ")",
  Object.keys(used).length > 0 && missing.length === 0);

// Top-level rules only (an @media body holds nested braces and is skipped).
var flat = style.replace(/@media[^{]*\{(?:[^{}]*\{[^{}]*\})*[^{}]*\}/g, "");
var rules = [];
flat.replace(/([^{}]+)\{([^{}]*)\}/g, function (_, sel, b) {
  rules.push({ sel: sel.split(",").map(function (s) { return s.trim(); }), body: b.replace(/\s+/g, "") });
});
function declsOf(sel) { return rules.filter(function (r) { return r.sel.indexOf(sel) >= 0; }).map(function (r) { return r.body; }).join(";"); }
function num(css, prop) { var m = css.match(new RegExp("(?:^|;)" + prop + ":(\\d+)")); return m ? +m[1] : null; }
var panel = declsOf(".dl-menu");
var panelAll = rules.filter(function (r) { return r.sel.some(function (s) { return /^\.dl-menu(?![\w-])/.test(s); }); }).map(function (r) { return r.body; }).join(";");
var dark = declsOf(":root");
var light = declsOf(':root[data-theme="light"]');
function menuBg(css) { var m = css.match(/(?:^|;)--menu-bg:([^;]+)/g); return m && m.length === 1 ? m[0].replace(/^;?--menu-bg:/, "") : ""; }
var thZ = num(declsOf(".dev-table th"), "z-index");
var menuZ = num(panel, "z-index");
var trigCss = declsOf(".dl-trigger");
var itemCss = declsOf(".dl-item");
check("E13 .dl-menu background var(--menu-bg), --menu-bg dark rgba(25,35,63,.9) and light rgba(227,236,248,.9), no gradient; " +
  "absolute, right:0, z-index " + menuZ + " > th " + thZ + ", max-width:calc(100vw - 32px); trigger and items 44px " +
  "(dark \"" + menuBg(dark) + "\", light \"" + menuBg(light) + "\")",
  /(?:^|;)background:var\(--menu-bg\)/.test(panel) && !/gradient|backdrop-filter/.test(panelAll) &&
  /^rgba\(25,35,63,0?\.90?\)$/.test(menuBg(dark)) && /^rgba\(227,236,248,0?\.90?\)$/.test(menuBg(light)) &&
  /(^|;)position:absolute/.test(panel) && /(^|;)right:0/.test(panel) &&
  thZ !== null && menuZ !== null && menuZ > thZ && /(^|;)max-width:calc\(100vw-32px\)/.test(panel) &&
  /(^|;)min-height:44px/.test(trigCss) && /(^|;)min-width:44px/.test(trigCss) && /(^|;)min-height:44px/.test(itemCss));

if (ran === 0) {
  console.error("FAIL: zero checks ran - refusing to pass silently");
  process.exit(1);
}
console.log((ran - failed) + "/" + ran + " passed, " + failed + " failed");
process.exit(failed ? 1 : 0);
