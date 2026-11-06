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
  diffOf: () => "", prettyArgs: () => "{}"
};
vm.createContext(context);
vm.runInContext(source.slice(source.indexOf("  function pre("), source.indexOf("  // ---------- full render")), context);
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
console.log("tool output ok");
