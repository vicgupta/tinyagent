"use strict";
const assert = require("assert");
const fs = require("fs");
const vm = require("vm");

// A minimal DOM exercises the real card functions, without adding a browser
// dependency to the CLI's embedded web UI tests.
class Element {
  constructor(tag, cls, text) {
    this.tag = tag; this.className = cls || ""; this.textContent = text || "";
    this.children = []; this.dataset = {}; this.open = false;
  }
  appendChild(child) { child.parent = this; this.children.push(child); return child; }
  remove() { this.parent.children = this.parent.children.filter(c => c !== this); }
  querySelector(selector) {
    const matches = n => selector.split(",").some(s => {
      s = s.trim();
      if (!s.startsWith(".")) return n.tag === s;
      return s.slice(1).split(".").every(c => n.className.split(" ").includes(c));
    });
    for (const child of this.children) {
      if (matches(child)) return child;
      const found = child.querySelector(selector); if (found) return found;
    }
    return null;
  }
}
const source = fs.readFileSync(__dirname + "/app.js", "utf8");
const context = {
  el: (tag, cls, text) => new Element(tag, cls, text),
  MD: { looksLikeDiff: () => false }, argSummary: () => "args",
  diffOf: () => "", prettyArgs: () => "{}",
  detailsOn: () => true,
  localStorage: (() => { const m = {}; return { getItem: k => (k in m ? m[k] : null), setItem: (k, v) => { m[k] = String(v); } }; })()
};
vm.createContext(context);
vm.runInContext(source.slice(source.indexOf("  function pre("), source.indexOf("  // ---------- full render")), context);
context.COLLAPSE_KEY = "tinyagent-collapse-tools";
// "Collapse tool calls" is checked by default: cards start as closed rows and
// stay closed for live output and errors, each still expandable on its own.
assert.equal(context.toolsCollapsed(), true, "collapsed by default");
const folded = context.toolCard("f1", "bash", {});
assert(!folded.open, "collapsed card starts closed");
context.toolOutput(folded, "chunk");
assert(!folded.open, "collapsed card stays closed for live output");
context.toolResult(folded, "boom", true);
assert(!folded.open && folded.querySelector(".st.err"), "collapsed error row keeps its status, stays closed");
folded.open = true; // a row expands on its own
const other = context.toolCard("f2", "grep", {});
assert(!other.open, "expanding one row leaves the others collapsed");
// Unchecked: tool calls and raw output are shown expanded inline.
context.localStorage.setItem(context.COLLAPSE_KEY, "0");
assert.equal(context.toolsCollapsed(), false);
assert(context.toolCard("e1", "bash", {}).open, "expanded mode opens cards");
const card = context.toolCard("build", "bash", {});
assert(!card.querySelector(".live-output"));
context.toolOutput(card, "first\n");
context.toolOutput(card, '<script>bad()</script>\n');
assert(card.open, "live card should open automatically");
assert.equal(card.querySelector(".live-output").querySelector("pre").textContent, "first\n<script>bad()</script>\n");
assert.equal(card.querySelector(".live-output").querySelector("pre").innerHTML, undefined, "output must be text, not HTML");
context.toolResult(card, "authoritative final", false);
assert(!card.querySelector(".live-output"), "final result must replace live preview");
assert.equal(card.children[card.children.length - 1].querySelector("pre").textContent, "authoritative final");
context.toolOutput(card, "late chunk");
assert(!card.querySelector(".live-output"), "completed cards ignore late chunks");
context.toolOutput(null, "orphan");
const failed = context.toolCard("failed", "build", {});
context.toolOutput(failed, "progress");
context.toolResult(failed, "failure", true);
assert(failed.open && failed.querySelector(".st.err"));
assert(!failed.querySelector(".live-output"));

// Exercise the real SSE handler too: attribution, append and scroll.
const handlers = {};
context.es = { addEventListener: (name, cb) => { handlers[name] = cb; } };
context.findCard = id => id === "next" ? (context.next || null) : null;
let scrolls = 0;
context.scroll = () => { scrolls++; };
vm.runInContext(source.slice(source.indexOf('    es.addEventListener("tool_output"'), source.indexOf('    es.addEventListener("tool_result"')), context);
context.next = context.toolCard("next", "bash", {});
handlers.tool_output({ data: JSON.stringify({ id: "next", name: "bash", output: "live SSE" }) });
assert.equal(context.next.querySelector(".live-output").querySelector("pre").textContent, "live SSE");
assert.equal(scrolls, 1);
// A handoff CLI has no preceding tool_call. tool_output opens the card.
const host = new Element("div", "msg");
context.S = { cur: { msg: host, seg: {} } };
handlers.tool_output({ data: JSON.stringify({ id: "cli-1", name: "Bash", output: "$ ls\n" }) });
const created = host.children[0];
assert.equal(created.dataset.tool, "cli-1");
assert.equal(created.querySelector(".name").textContent, "Bash");
assert.equal(created.querySelector(".live-output").querySelector("pre").textContent, "$ ls\n");
const previous = context.findCard;
context.findCard = id => id === "cli-1" ? created : previous(id);
handlers.tool_output({ data: JSON.stringify({ id: "cli-1", name: "Bash", output: "a.go\n" }) });
assert.equal(host.children.length, 1, "a later chunk must reuse the card");
assert.equal(created.querySelector(".live-output").querySelector("pre").textContent, "$ ls\na.go\n");
// Without -v the page still shows tool output (collapsed by default).
context.detailsOn = () => false;
context.localStorage.setItem(context.COLLAPSE_KEY, "1");
handlers.tool_output({ data: JSON.stringify({ id: "quiet", name: "bash", output: "quiet-output" }) });
assert.equal(host.children.length, 2, "non-verbose mode shows tool output too");
const quiet = host.children[1];
assert(!quiet.open, "non-verbose tool output starts collapsed");
assert.equal(quiet.querySelector(".live-output").querySelector("pre").textContent, "quiet-output");
// The checkbox folds or unfolds every card already on the page, including
// verbose detail cards, and remembers the choice.
const cards = [new Element("details", "tool"), new Element("details", "tool detail")];
cards[0].open = true;
context.els = { msgs: { querySelectorAll: sel => { assert.equal(sel, "details.tool"); return cards; } }, collapse: { checked: false } };
vm.runInContext(source.slice(source.indexOf("  // ---------- collapse tool calls"), source.indexOf("  // ---------- theme")), context);
context.setCollapsed(false);
assert(cards.every(c => c.open) && context.localStorage.getItem(context.COLLAPSE_KEY) === "0" && !context.els.collapse.checked);
context.setCollapsed(true);
assert(cards.every(c => !c.open) && context.localStorage.getItem(context.COLLAPSE_KEY) === "1" && context.els.collapse.checked);
assert.equal(context.toolsCollapsed(), true);
console.log("tool output ok");
