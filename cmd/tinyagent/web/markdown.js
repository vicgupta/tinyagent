// A small, dependency-free Markdown renderer and syntax highlighter for the
// TinyAgent web UI. Input is always escaped first, so model output can't
// inject HTML; links are limited to http(s) and mailto.
(function () {
  "use strict";
  function esc(s) {
    return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#39;");
  }

  var KW = {
    go: "break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var nil true false iota",
    js: "await break case catch class const continue debugger default delete do else export extends finally for function if import in instanceof let new null return super switch this throw true false try typeof undefined var void while with yield async of",
    py: "and as assert async await break class continue def del elif else except False finally for from global if import in is lambda None nonlocal not or pass raise return True try while with yield self",
    sh: "if then else elif fi for while do done case esac in function return export local echo exit set unset cd",
    rust: "as async await break const continue crate else enum extern false fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait true type unsafe use where while",
    c: "auto break case char const continue default do double else enum extern float for goto if int long register return short signed sizeof static struct switch typedef union unsigned void volatile while class public private protected template typename namespace using new delete true false nullptr bool",
    sql: "select from where and or not insert into values update set delete create table index drop alter join left right inner outer on group by order limit as null is in like distinct having union",
  };
  var ALIAS = { golang: "go", javascript: "js", ts: "js", typescript: "js", jsx: "js", tsx: "js", json: "js", python: "py", bash: "sh", shell: "sh", zsh: "sh", console: "sh", rs: "rust", cpp: "c", "c++": "c", java: "c", cs: "c", h: "c" };
  var sets = {};
  Object.keys(KW).forEach(function (k) { sets[k] = new Set(KW[k].split(" ")); });

  function highlight(code, lang) {
    lang = (lang || "").toLowerCase();
    if (lang === "diff" || lang === "patch" || (!lang && looksLikeDiff(code))) return highlightDiff(code);
    var key = ALIAS[lang] || lang;
    var words = sets[key] || (lang ? sets.js : null);
    if (!words) return esc(code);
    var hashComment = key === "py" || key === "sh" || lang === "yaml" || lang === "toml" || lang === "ruby";
    var re = hashComment
      ? /(#[^\n]*)|("(?:\\.|[^"\\\n])*"|'(?:\\.|[^'\\\n])*')|\b(\d[\d_.xXa-fA-F]*)\b|([A-Za-z_][\w]*)/g
      : /(\/\/[^\n]*|\/\*[\s\S]*?\*\/)|("(?:\\.|[^"\\\n])*"|'(?:\\.|[^'\\\n])*'|`[^`]*`)|\b(\d[\d_.xXa-fA-F]*)\b|([A-Za-z_$][\w$]*)/g;
    var out = "", last = 0, m;
    while ((m = re.exec(code))) {
      out += esc(code.slice(last, m.index));
      if (m[1]) out += '<span class="tok-c">' + esc(m[1]) + "</span>";
      else if (m[2]) out += '<span class="tok-s">' + esc(m[2]) + "</span>";
      else if (m[3]) out += '<span class="tok-n">' + esc(m[3]) + "</span>";
      else if (m[4]) out += words.has(m[4]) || words.has(m[4].toLowerCase()) && key === "sql" ? '<span class="tok-k">' + esc(m[4]) + "</span>" : esc(m[4]);
      last = re.lastIndex;
    }
    return out + esc(code.slice(last));
  }

  function looksLikeDiff(s) {
    return /^(--- |\+\+\+ |@@ |\*\*\* (Begin|Update|Add|Delete) )/m.test(s) && /^[+-]/m.test(s);
  }

  function highlightDiff(code) {
    return code.split("\n").map(function (l) {
      var c = "";
      if (/^(\+\+\+|---|\*\*\*)/.test(l)) c = "tok-dh";
      else if (l[0] === "+") c = "tok-da";
      else if (l[0] === "-") c = "tok-dd";
      else if (l.slice(0, 2) === "@@") c = "tok-dh";
      return c ? '<span class="' + c + '">' + esc(l) + "</span>" : esc(l);
    }).join("\n");
  }

  // safeURL allows only http(s) and mailto. Control characters and whitespace
  // are stripped first so a scheme can't be hidden inside them, and every
  // other scheme (javascript, data, vbscript) is dropped.
  function safeURL(u) {
    u = String(u).replace(/[\u0000-\u001F\u007F\s]/g, "");
    var m = /^([a-z][a-z0-9+.-]*):/i.exec(u);
    if (!m) return null;
    var scheme = m[1].toLowerCase();
    if (scheme !== "http" && scheme !== "https" && scheme !== "mailto") return null;
    return u;
  }

  function link(text, url) {
    var u = safeURL(String(url).replace(/&amp;/g, "&"));
    if (!u) return null;
    return '<a href="' + esc(u) + '" target="_blank" rel="noopener noreferrer">' + text + "</a>";
  }

  function inline(s) {
    var codes = [];
    s = s.replace(/`([^`\n]+)`/g, function (_, c) { codes.push(c); return "\u0000" + (codes.length - 1) + "\u0000"; });
    s = esc(s);
    // Images stay links (or their alt text). An <img> could point at a
    // script URL; the page never inserts one from model text.
    s = s.replace(/!\[([^\]]*)\]\(([^)\s]+)\)/g, function (all, text, url) {
      return link(text || esc(url), url) || text;
    });
    s = s.replace(/\[([^\]\n]+)\]\(([^)\s]+)\)/g, function (all, text, url) {
      return link(text, url) || all;
    });
    s = s.replace(/(^|[\s(])(https?:\/\/[^\s<)]+)/g, '$1<a href="$2" target="_blank" rel="noopener noreferrer">$2</a>');
    s = s.replace(/\*\*([^*\n]+)\*\*/g, "<strong>$1</strong>").replace(/__([^_\n]+)__/g, "<strong>$1</strong>");
    s = s.replace(/(^|[^*\w])\*([^*\n]+)\*(?!\w)/g, "$1<em>$2</em>").replace(/(^|[^_\w])_([^_\n]+)_(?!\w)/g, "$1<em>$2</em>");
    s = s.replace(/~~([^~\n]+)~~/g, "<del>$1</del>");
    return s.replace(/\u0000(\d+)\u0000/g, function (_, i) { return "<code>" + esc(codes[+i]) + "</code>"; });
  }

  function codeBlock(code, lang) {
    return '<div class="code"><div class="code-head"><span>' + esc(lang || "text") + '</span><button type="button" class="copy" data-copy>Copy</button></div><pre><code>' + highlight(code, lang) + "</code></pre></div>";
  }

  // render turns Markdown into HTML. An unclosed fence (mid-stream) renders
  // as a code block too.
  function render(src) {
    var lines = src.replace(/\r\n/g, "\n").split("\n");
    var out = [], i = 0, para = [];
    function flush() {
      if (para.length) { out.push("<p>" + inline(para.join("\n")).replace(/\n/g, "<br>") + "</p>"); para = []; }
    }
    while (i < lines.length) {
      var l = lines[i], m;
      if ((m = /^\s*(```|~~~)\s*([\w+#.-]*)/.exec(l))) {
        flush();
        var fence = m[1], lang = m[2], buf = [];
        i++;
        while (i < lines.length && lines[i].trim().indexOf(fence) !== 0) buf.push(lines[i++]);
        i++;
        out.push(codeBlock(buf.join("\n"), lang));
        continue;
      }
      if ((m = /^(#{1,6})\s+(.*)$/.exec(l))) { flush(); out.push("<h" + m[1].length + ">" + inline(m[2]) + "</h" + m[1].length + ">"); i++; continue; }
      if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(l)) { flush(); out.push("<hr>"); i++; continue; }
      if (/^\s*>/.test(l)) {
        flush();
        var q = [];
        while (i < lines.length && /^\s*>/.test(lines[i])) q.push(lines[i++].replace(/^\s*>\s?/, ""));
        out.push("<blockquote>" + render(q.join("\n")) + "</blockquote>");
        continue;
      }
      if (/^\s*\|.*\|\s*$/.test(l) && i + 1 < lines.length && /^\s*\|?\s*:?-+/.test(lines[i + 1])) {
        flush();
        var row = function (r) { return r.trim().replace(/^\||\|$/g, "").split("|").map(function (c) { return c.trim(); }); };
        var head = row(l); i += 2;
        var t = '<div class="table-wrap"><table><thead><tr>' + head.map(function (c) { return "<th>" + inline(c) + "</th>"; }).join("") + "</tr></thead><tbody>";
        while (i < lines.length && /^\s*\|.*\|\s*$/.test(lines[i])) t += "<tr>" + row(lines[i++]).map(function (c) { return "<td>" + inline(c) + "</td>"; }).join("") + "</tr>";
        out.push(t + "</tbody></table></div>");
        continue;
      }
      if ((m = /^(\s*)([-*+]|\d+[.)])\s+/.exec(l))) {
        flush();
        var ordered = /\d/.test(m[2]), items = [], cur = null;
        while (i < lines.length) {
          var lm = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/.exec(lines[i]);
          if (lm && lm[1].length <= m[1].length + 1) { cur = [lm[3]]; items.push(cur); i++; continue; }
          if (cur && lines[i].trim() !== "" && /^\s+/.test(lines[i])) { cur.push(lines[i].replace(/^\s{1,4}/, "")); i++; continue; }
          break;
        }
        out.push((ordered ? "<ol>" : "<ul>") + items.map(function (it) {
          var body = it.length > 1 ? render(it.join("\n")) : inline(it[0]);
          body = body.replace(/^\[([ xX])\]\s/, function (_, x) { return '<input type="checkbox" disabled' + (x === " " ? "" : " checked") + "> "; });
          return "<li>" + body + "</li>";
        }).join("") + (ordered ? "</ol>" : "</ul>"));
        continue;
      }
      if (l.trim() === "") { flush(); i++; continue; }
      para.push(l); i++;
    }
    flush();
    return out.join("\n");
  }

  window.TAMarkdown = { render: render, highlight: highlight, escape: esc, looksLikeDiff: looksLikeDiff };
})();
