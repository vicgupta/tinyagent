// Checks for the web UI's Markdown renderer and highlighter; run by
// TestWebUIScripts when node is installed: node web/ui_test.js
"use strict";
const assert = require("assert");
global.window = {};
require("./markdown.js");
const { render, highlight, looksLikeDiff } = window.TAMarkdown;

// Model output can't inject HTML or script URLs.
let h = render("<img src=x onerror=alert(1)> [x](javascript:alert(1)) <script>bad()</script>");
assert(!/<img|<script/i.test(h), h);
assert(!/href="javascript/i.test(h), h);
h = render("```html\n<b>\"q\"</b>\n```");
assert(h.includes("&lt;b&gt;") && !h.includes("<b>"), h);

// Structure.
h = render("# T\n\n- a\n- [x] done\n\n1. one\n2. two\n\n> quote\n\n|a|b|\n|-|-|\n|1|2|\n\n---\n**b** *i* `c` ~~s~~ [l](https://example.com)");
for (const want of ["<h1>T</h1>", "<ul>", "<ol>", "checked", "<blockquote>", "<table>", "<hr>", "<strong>b</strong>", "<em>i</em>", "<code>c</code>", "<del>s</del>", 'href="https://example.com"', 'rel="noopener noreferrer"']) {
  assert(h.includes(want), want + " missing in " + h);
}

// Code blocks get a copy button and highlighting; an unclosed fence
// (mid-stream) still renders as code.
h = render("```go\nfunc main() { return \"x\" } // c\n```");
assert(h.includes("data-copy") && h.includes('<span class="tok-k">func</span>') && h.includes("tok-s") && h.includes("tok-c"), h);
h = render("text\n```python\ndef f():");
assert(h.includes('<span class="tok-k">def</span>'), h);

// Diffs.
const diff = "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new";
assert(looksLikeDiff(diff));
h = highlight(diff, "");
assert(h.includes('class="tok-dd">-old') && h.includes('class="tok-da">+new'), h);
assert(!looksLikeDiff("just - a list\n+ plus"));

console.log("ok");

require("./tool_output_test.js");
