// Checks for the web UI's Markdown renderer and highlighter; run by
// TestWebUIScripts when node is installed: node web/ui_test.js
"use strict";
const assert = require("assert");
global.window = {};
require("./markdown.js");
const { render, highlight, looksLikeDiff } = window.TAMarkdown;

// Model output can't inject HTML. Links are only http(s) or mailto; an
// unsafe URL is left as text, so the scheme may appear outside an href.
function assertSafe(src) {
  const html = render(src);
  assert(!/<(?:script|img|svg|iframe|object)\b/i.test(html), html);
  assert(!/<\w[^>]*\son\w+=/i.test(html), html);
  for (const href of html.match(/href="[^"]*"/gi) || []) {
    assert(/href="(?:https?:|mailto:)/i.test(href), href + " in " + html);
    assert(!/javascript:|data:|vbscript:/i.test(href), href);
  }
  return html;
}
let h = assertSafe("<img src=x onerror=alert(1)> [x](javascript:alert(1)) <script>bad()</script>");
h = assertSafe('[x](JaVaScRiPt:alert(1)) [x](data:text/html,<script>bad()</script>) [x](vbscript:msgbox) [x](java\nscript:alert(1))');
h = assertSafe("> <svg onload=alert(1)>\n\n- <img src=x onerror=alert(1)>\n\n| <script>bad()</script> |\n| --- |\n| x |");
assert(h.includes("&lt;svg") && h.includes("&lt;img") && h.includes("&lt;script"), h);
h = render("[mail](mailto:a@b.c) [ok](https://example.com) ![pic](https://example.com/a.png) ![x](javascript:alert(1))");
assert(h.includes('href="mailto:a@b.c"') && h.includes('href="https://example.com"') && h.includes('href="https://example.com/a.png"'), h);
assert(!/<img/i.test(h) && !/javascript:/i.test(h), h);
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
h = render("## Section\n\n### Smaller\n\n- a\n  - nested\n\n1. one\n   - under");
assert(h.includes("<h2>Section</h2>") && h.includes("<h3>Smaller</h3>") && h.includes("<ul><li>nested</li></ul>") && h.includes("<ul><li>under</li></ul>"), h);
h = render("| <b>no</b> | **yes** |\n| --- | --- |\n| 1 | 2 |");
assert(h.includes('class="table-wrap"') && h.includes("<strong>yes</strong>") && h.includes("&lt;b&gt;"), h);

// Diffs.
const diff = "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new";
assert(looksLikeDiff(diff));
h = highlight(diff, "");
assert(h.includes('class="tok-dd">-old') && h.includes('class="tok-da">+new'), h);
assert(!looksLikeDiff("just - a list\n+ plus"));

console.log("ok");

require("./tool_output_test.js");
