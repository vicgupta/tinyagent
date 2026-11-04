// TinyAgent web UI. Plain JavaScript, no build step. All requests carry the
// per-run token; the server streams events over SSE (/api/events).
(function () {
  "use strict";
  var MD = window.TAMarkdown;
  var $ = function (id) { return document.getElementById(id); };
  var params = new URLSearchParams(location.search);
  var token = params.get("token") || sessionStorage.getItem("tinyagent-token") || "";
  if (params.get("token")) {
    sessionStorage.setItem("tinyagent-token", token);
    history.replaceState(null, "", "/"); // keep the token out of the address bar and history
  }

  var S = { state: null, commands: [], history: [], raw: false, running: false, cur: null, pinned: true, attachments: [], perms: {}, connected: false };
  var els = {
    app: $("app"), log: $("log"), msgs: $("messages"), empty: $("empty"), input: $("composer-input"), form: $("composer"),
    send: $("send"), stop: $("stop"), status: $("status"), banner: $("banner"), jump: $("jump"), slash: $("slash-menu"),
    model: $("model-input"), modelList: $("model-list"), sessions: $("sessions"), search: $("session-search"),
    palette: $("palette"), paletteInput: $("palette-input"), paletteList: $("palette-list"), help: $("help"), helpBody: $("help-body"),
    project: $("project"), projectBody: $("project-body"), attachments: $("attachments"), file: $("file-input"), drop: $("drop"),
  };

  // ---------- API ----------
  function api(path, body) {
    var opt = { headers: { "X-TinyAgent-Token": token } };
    if (body !== undefined) { opt.method = "POST"; opt.headers["Content-Type"] = "application/json"; opt.body = JSON.stringify(body); }
    return fetch(path, opt).then(function (r) {
      return r.text().then(function (t) {
        var v; try { v = JSON.parse(t); } catch (e) { v = { error: t.trim() || r.statusText }; }
        if (!r.ok) { var err = new Error(v.error || r.statusText); err.status = r.status; throw err; }
        return v;
      });
    });
  }
  function fail(e) { notice(e.message || String(e), "error"); }

  // ---------- rendering helpers ----------
  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }
  function setMD(node, text) {
    node.classList.toggle("raw", S.raw || (S.state && !S.state.markdown));
    if (S.raw || (S.state && !S.state.markdown)) node.textContent = text;
    else node.innerHTML = MD.render(text);
    node.dataset.src = text;
  }
  function nearBottom() { return els.log.scrollHeight - els.log.scrollTop - els.log.clientHeight < 80; }
  function scroll() { if (S.pinned) els.log.scrollTop = els.log.scrollHeight; updateJump(); }
  function updateJump() { els.jump.hidden = nearBottom(); }
  function showEmpty() { els.empty.hidden = els.msgs.children.length > 0; }
  function append(node) { els.msgs.appendChild(node); showEmpty(); scroll(); return node; }

  function notice(text, kind) {
    var m = el("div", "msg " + (kind === "error" ? "error" : kind === "log" ? "log" : "notice"));
    var b = el("div", "body", text);
    m.appendChild(b);
    if (kind === "error") m.setAttribute("role", "alert");
    return append(m);
  }

  function userMsg(text, images, last) {
    var m = el("div", "msg user");
    var who = el("div", "who", "You");
    var acts = el("span", "actions");
    var edit = el("button", "btn ghost small", "Edit");
    edit.type = "button"; edit.dataset.act = "edit"; edit.setAttribute("aria-label", "Edit and resend");
    var copy = el("button", "btn ghost small", "Copy");
    copy.type = "button"; copy.dataset.act = "copy-msg";
    acts.appendChild(copy); acts.appendChild(edit);
    who.appendChild(acts);
    m.appendChild(who);
    var body = el("div", "body", text + (images ? "\n[" + images + " image" + (images > 1 ? "s" : "") + "]" : ""));
    body.dataset.src = text;
    m.appendChild(body);
    if (last) m.dataset.last = "1";
    return m;
  }

  function assistantMsg() {
    var m = el("div", "msg assistant");
    var who = el("div", "who", "TinyAgent");
    var acts = el("span", "actions");
    var copy = el("button", "btn ghost small", "Copy"); copy.type = "button"; copy.dataset.act = "copy-msg";
    var regen = el("button", "btn ghost small", "Regenerate"); regen.type = "button"; regen.dataset.act = "regen";
    acts.appendChild(copy); acts.appendChild(regen);
    who.appendChild(acts);
    m.appendChild(who);
    return m;
  }

  function segment(m) {
    var d = el("div", "md");
    d.dataset.src = "";
    m.appendChild(d);
    return d;
  }

  function prettyArgs(a) {
    if (a == null) return "";
    if (typeof a !== "string") return JSON.stringify(a, null, 2);
    try { return JSON.stringify(JSON.parse(a), null, 2); } catch (e) { return a; }
  }
  function argSummary(a) {
    var o = a;
    if (typeof a === "string") { try { o = JSON.parse(a); } catch (e) { return a.slice(0, 140); } }
    if (o && typeof o === "object") {
      var k = ["path", "command", "cmd", "pattern", "query", "url", "file", "question"].find(function (x) { return typeof o[x] === "string"; });
      if (k) return o[k].split("\n")[0].slice(0, 140);
      return JSON.stringify(o).slice(0, 140);
    }
    return String(o).slice(0, 140);
  }
  function diffOf(args) {
    var o = args;
    if (typeof o === "string") { try { o = JSON.parse(o); } catch (e) { return MD.looksLikeDiff(o) ? o : ""; } }
    if (!o || typeof o !== "object") return "";
    for (var k in o) if (typeof o[k] === "string" && MD.looksLikeDiff(o[k])) return o[k];
    var oldS = o.old_string || o.old_str || o.old || o.search, newS = o.new_string || o.new_str || o.new || o.replace;
    if (typeof oldS === "string" && typeof newS === "string") {
      return "--- " + (o.path || "before") + "\n+++ " + (o.path || "after") + "\n" +
        oldS.split("\n").map(function (l) { return "-" + l; }).join("\n") + "\n" + newS.split("\n").map(function (l) { return "+" + l; }).join("\n");
    }
    return "";
  }
  function pre(text, diff) {
    var p = el("pre");
    if (diff || MD.looksLikeDiff(text)) p.innerHTML = MD.highlight(text, "diff"); else p.textContent = text;
    return p;
  }

  function toolCard(id, name, args) {
    var d = el("details", "tool");
    d.dataset.tool = id || "";
    var s = el("summary");
    s.appendChild(el("span", "name", name || "tool"));
    s.appendChild(el("span", "arg", argSummary(args)));
    var st = el("span", "st run", "running…");
    s.appendChild(st);
    d.appendChild(s);
    var diff = diffOf(args);
    if (diff) { var ds = el("div", "section"); ds.appendChild(el("h4", null, "Diff")); ds.appendChild(pre(diff, true)); d.appendChild(ds); }
    var as = el("div", "section"); as.appendChild(el("h4", null, "Arguments")); as.appendChild(pre(prettyArgs(args))); d.appendChild(as);
    return d;
  }
  function toolResult(card, output, isErr) {
    if (!card) return;
    var st = card.querySelector(".st");
    st.className = "st " + (isErr ? "err" : "ok");
    st.textContent = isErr ? "error" : "done";
    var rs = el("div", "section"); rs.appendChild(el("h4", null, "Result")); rs.appendChild(pre(output || "(no output)")); card.appendChild(rs);
    if (isErr) card.open = true;
  }
  function findCard(id) {
    var all = els.msgs.querySelectorAll("details.tool");
    for (var i = all.length - 1; i >= 0; i--) if (all[i].dataset.tool === String(id) && !all[i].querySelector(".st.ok, .st.err")) return all[i];
    return null;
  }

  // ---------- full render from /api/state ----------
  function renderAll() {
    var st = S.state;
    els.msgs.textContent = "";
    var msgs = st.messages || [];
    var lastUser = -1;
    msgs.forEach(function (m, i) { if (m.role === "user") lastUser = i; });
    var a = null;
    msgs.forEach(function (m, i) {
      if (m.role === "user") { a = null; els.msgs.appendChild(userMsg(m.content || "", m.images, i === lastUser)); return; }
      if (m.role === "assistant") {
        if (!a) { a = assistantMsg(); els.msgs.appendChild(a); }
        if (m.content && m.content.trim()) setMD(segment(a), m.content);
        (m.tool_calls || []).forEach(function (tc) {
          var fn = tc.function || tc; a.appendChild(toolCard(tc.id, fn.name || tc.name, fn.arguments != null ? fn.arguments : tc.arguments));
        });
        return;
      }
      if (m.role === "tool") { toolResult(findCard(m.tool_call_id), m.content, /^(error|Error)/.test(m.content || "")); }
    });
    if (st.question) questionCard(st.question);
    Object.keys(S.perms).forEach(function (id) { if (!S.perms[id].done) permCard(S.perms[id].ev); });
    showEmpty();
    S.pinned = true; scroll();
    markLast();
  }
  function markLast() {
    var users = els.msgs.querySelectorAll(".msg.user");
    users.forEach(function (u, i) { u.querySelector("[data-act=edit]").hidden = i !== users.length - 1 || !canRegen(); });
    var as = els.msgs.querySelectorAll(".msg.assistant");
    as.forEach(function (x, i) { x.querySelector("[data-act=regen]").hidden = i !== as.length - 1 || !canRegen(); });
  }
  function canRegen() { return S.state && S.state.canRegen && !S.running; }

  function renderStatus() {
    var st = S.state; if (!st) return;
    els.status.textContent = "";
    function item(label, val, title) { var s = el("span", null); s.appendChild(document.createTextNode(label + " ")); var c = el("code", null, val); s.appendChild(c); if (title) s.title = title; els.status.appendChild(s); }
    item("model", st.model);
    if (st.handoff) { var hb = el("span", "badge on", "handoff"); hb.title = "This model hands each message to its own CLI and returns only the final answer: no tool cards, and TinyAgent's tools, sandbox and subagents don't apply."; els.status.appendChild(hb); }
    item("cwd", st.cwd);
    item("turns", String(st.turns));
    var u = st.usage || {};
    item("tokens", fmtInt(u.input_tokens || 0) + " in / " + fmtInt(u.output_tokens || 0) + " out");
    item("cost", u.cost ? "$" + u.cost.toFixed(4) : "—");
    if (st.checkpoint) item("checkpoint", st.checkpoint);
    var set = st.settings || {};
    ["sandbox", "read_only", "lockdown", "permissions", "memory", "ask_user"].forEach(function (k) {
      if (set[k]) els.status.appendChild(el("span", "badge on", k.replace("_", "-")));
    });
    if (st.compact) els.status.appendChild(el("span", "badge", "compact next"));
    if (S.raw) els.status.appendChild(el("span", "badge", "raw"));
    if (!S.connected) els.status.appendChild(el("span", "badge", "offline"));
  }
  function fmtInt(n) { return Number(n).toLocaleString(); }

  function applyState(st) {
    S.state = st;
    setRunning(st.running);
    if (document.activeElement !== els.model) els.model.value = st.model;
    els.modelList.textContent = "";
    (st.models || []).forEach(function (m) { var o = el("option"); o.value = m; els.modelList.appendChild(o); });
    document.title = "TinyAgent · " + (st.cwd || "").split(/[\\/]/).pop();
    renderStatus();
  }
  function loadState(render) {
    return api("/api/state").then(function (st) { applyState(st); if (render) renderAll(); else markLast(); }).catch(fail);
  }
  function setRunning(r) {
    S.running = !!r;
    els.stop.hidden = !S.running;
    els.send.hidden = S.running;
    els.model.disabled = S.running;
    $("model-apply").disabled = S.running;
    markLast();
  }

  // ---------- questions and permission prompts ----------
  function questionCard(q) {
    var c = el("div", "card");
    c.setAttribute("role", "group"); c.setAttribute("aria-label", "Question from the agent");
    c.appendChild(el("strong", null, "The agent asks: "));
    c.appendChild(el("div", null, q.question));
    var row = el("div", "row");
    (q.options || []).forEach(function (o) {
      var b = el("button", "btn", o); b.type = "button";
      b.addEventListener("click", function () { c.classList.add("done"); sendText(o); });
      row.appendChild(b);
    });
    var hint = el("span", null, (q.options || []).length ? "or type an answer below" : "type your answer below");
    hint.className = "m"; row.appendChild(hint);
    c.appendChild(row);
    append(c);
    els.input.focus();
  }

  function permCard(ev) {
    var c = el("div", "card perm");
    c.dataset.perm = ev.id;
    c.setAttribute("role", "alertdialog"); c.setAttribute("aria-label", "Permission request for " + ev.tool);
    c.appendChild(el("strong", null, "Allow " + ev.tool + (ev.subagent ? " (subagent " + ev.subagent + ")" : "") + "?"));
    if (ev.rule) c.appendChild(el("div", "m", "rule: " + ev.rule));
    if (ev.argument) c.appendChild(pre(String(ev.argument)));
    var row = el("div", "row");
    var reason = el("input", "input"); reason.placeholder = "Reason for denying (optional, sent to the agent)"; reason.setAttribute("aria-label", "Deny reason");
    [["allow", "Allow once", "btn primary"], ["always", "Always allow", "btn"], ["deny", "Deny", "btn danger"]].forEach(function (d) {
      var b = el("button", d[2], d[1]); b.type = "button"; b.dataset.decision = d[0];
      b.addEventListener("click", function () {
        api("/api/permission", { id: ev.id, decision: d[0], reason: d[0] === "deny" ? reason.value : "" }).catch(fail);
      });
      row.appendChild(b);
    });
    row.appendChild(reason);
    c.appendChild(row);
    append(c);
    var first = c.querySelector("button"); if (first) first.focus();
  }
  function permDone(id, decision) {
    if (S.perms[id]) S.perms[id].done = true;
    var c = els.msgs.querySelector('[data-perm="' + CSS.escape(id) + '"]');
    if (!c) return;
    c.classList.add("done");
    c.querySelectorAll("button, input").forEach(function (b) { b.disabled = true; });
    c.appendChild(el("div", "m", decision === "cancelled" ? "No answer before the turn ended." : "Answered: " + decision));
  }

  // ---------- events ----------
  var es = null, retry = 0;
  function connect() {
    if (es) es.close();
    es = new EventSource("/api/events?token=" + encodeURIComponent(token));
    es.addEventListener("hello", function (e) {
      var wasDown = !S.connected && retry > 0;
      S.connected = true; retry = 0;
      banner("");
      S.perms = {};
      loadState(true).then(function () { if (wasDown) notice("Reconnected."); });
    });
    es.addEventListener("state", function () { loadState(true); refreshHistory(); });
    es.addEventListener("turn_start", function (e) {
      var d = JSON.parse(e.data);
      if (d.rewind) {
        var lastU = els.msgs.querySelectorAll(".msg.user");
        var u = lastU[lastU.length - 1];
        while (u && u.nextSibling) u.nextSibling.remove();
        if (u) u.remove();
      }
      els.msgs.querySelectorAll(".card:not(.done)").forEach(function (c) { if (!c.dataset.perm) c.classList.add("done"); });
      append(userMsg(d.message, d.images, true));
      S.cur = { msg: append(assistantMsg()), seg: null, text: "" };
      S.cur.msg.classList.add("cursor");
      setRunning(true);
    });
    es.addEventListener("delta", function (e) {
      var d = JSON.parse(e.data);
      if (!S.cur) S.cur = { msg: append(assistantMsg()), seg: null };
      if (!S.cur.seg) { S.cur.seg = segment(S.cur.msg); }
      var pinned = nearBottom();
      setMD(S.cur.seg, S.cur.seg.dataset.src + d.text);
      S.pinned = pinned; scroll();
    });
    es.addEventListener("tool_call", function (e) {
      var d = JSON.parse(e.data);
      if (!S.cur) S.cur = { msg: append(assistantMsg()), seg: null };
      S.cur.seg = null;
      S.cur.msg.appendChild(toolCard(d.id, d.name, d.arguments));
      scroll();
    });
    es.addEventListener("tool_result", function (e) {
      var d = JSON.parse(e.data);
      toolResult(findCard(d.id), d.output, d.is_error); scroll();
    });
    es.addEventListener("log", function (e) { notice(JSON.parse(e.data).text, "log"); });
    es.addEventListener("notice", function (e) { notice(JSON.parse(e.data).text); });
    es.addEventListener("warning", function (e) { notice(JSON.parse(e.data).text, "error"); });
    es.addEventListener("permission", function (e) {
      var d = JSON.parse(e.data);
      if (S.perms[d.id]) return;
      S.perms[d.id] = { ev: d };
      permCard(d);
    });
    es.addEventListener("permission_done", function (e) { var d = JSON.parse(e.data); permDone(d.id, d.decision); });
    es.addEventListener("turn_end", function (e) {
      var d = JSON.parse(e.data);
      var cur = S.cur; S.cur = null;
      if (cur) {
        cur.msg.classList.remove("cursor");
        if (!d.streamed && d.submission && d.status !== "Error") setMD(segment(cur.msg), d.submission);
        if (!cur.msg.querySelector(".md, details")) cur.msg.remove();
      }
      if (d.error) notice(d.error + (d.hint ? "\n" + d.hint : ""), "error");
      if (d.status === "Interrupted") notice("Stopped.");
      if (d.line) append(el("div", "msg turnline", d.line));
      if (d.question) questionCard(d.question);
      setRunning(false);
      loadState(false);
      refreshHistory();
    });
    es.onerror = function () {
      if (S.connected) { S.connected = false; renderStatus(); }
      retry++;
      banner("Connection to tinyagent web lost — reconnecting… (if the server stopped, restart it and reopen the URL it prints)");
      if (es.readyState === EventSource.CLOSED) setTimeout(connect, Math.min(10000, 500 * retry));
    };
  }
  function banner(text, kind) {
    els.banner.hidden = !text;
    els.banner.textContent = text;
    els.banner.className = "banner" + (kind ? " " + kind : "");
  }

  // ---------- sending ----------
  function sendText(text) {
    var imgs = S.attachments.filter(function (a) { return a.image; }).map(function (a) { return { name: a.name, data: a.data }; });
    if (!text.trim() && !imgs.length) return;
    var trimmed = text.trim();
    if (trimmed[0] === "/" && trimmed[1] !== "/" && !imgs.length) { runCommand(trimmed); els.input.value = ""; autosize(); return; }
    api("/api/message", { text: text, images: imgs }).then(function () {
      els.input.value = ""; autosize(); S.attachments = []; renderAttachments(); S.pinned = true;
    }).catch(fail);
  }
  function runCommand(line) {
    var name = line.split(/\s+/)[0];
    if (name === "/help") { openHelp(); return; }
    if (name === "/history") { openSidebar(); els.search.focus(); return; }
    if (name === "/raw") { S.raw = !S.raw; notice("raw Markdown " + (S.raw ? "on" : "off")); rerenderMD(); renderStatus(); return; }
    if (name === "/stop") { stop(); return; }
    if (name === "/model" && line.trim() === "/model") { els.model.focus(); els.model.select(); return; }
    api("/api/command", { line: line }).then(function (r) {
      if (name === "/exit" || name === "/quit") { banner("tinyagent web stopped. You can close this tab.", "error"); if (es) es.close(); }
      loadState(false);
    }).catch(fail);
  }
  function rerenderMD() { els.msgs.querySelectorAll(".md").forEach(function (n) { setMD(n, n.dataset.src || ""); }); }
  function stop() { api("/api/cancel", {}).catch(fail); }
  function switchModel(spec) {
    spec = (spec || "").trim(); if (!spec) return;
    api("/api/model", { model: spec }).then(function () { notice("model: " + spec); return loadState(false); }).catch(function (e) { fail(e); if (S.state) els.model.value = S.state.model; });
  }
  function newChat() { runCommand("/new"); els.input.focus(); }

  // ---------- attachments ----------
  var MAX_TEXT = 512 * 1024;
  function addFiles(files) {
    Array.prototype.forEach.call(files, function (f) {
      var reader = new FileReader();
      if (/^image\/(png|jpeg|gif|webp)$/.test(f.type)) {
        reader.onload = function () { S.attachments.push({ name: f.name || "pasted image", data: reader.result, image: true }); renderAttachments(); };
        reader.readAsDataURL(f);
      } else if (f.size <= MAX_TEXT) {
        reader.onload = function () {
          var txt = String(reader.result);
          if (/\u0000/.test(txt)) { notice(f.name + ": binary files can't be attached (only images and text)", "error"); return; }
          var ext = (f.name.split(".").pop() || "").toLowerCase();
          insertText("\n`" + f.name + "`:\n```" + ext + "\n" + txt.replace(/\n$/, "") + "\n```\n");
        };
        reader.readAsText(f);
      } else notice(f.name + " is too large to inline (max 512 KB); reference its path instead", "error");
    });
  }
  function insertText(t) {
    var i = els.input, s = i.selectionStart, e = i.selectionEnd;
    i.value = i.value.slice(0, s) + t + i.value.slice(e);
    i.selectionStart = i.selectionEnd = s + t.length; autosize(); i.focus();
  }
  function renderAttachments() {
    els.attachments.textContent = "";
    S.attachments.forEach(function (a, i) {
      var c = el("span", "chip");
      var img = el("img"); img.src = a.data; img.alt = ""; c.appendChild(img);
      c.appendChild(el("span", null, a.name));
      var x = el("button", null, "×"); x.type = "button"; x.setAttribute("aria-label", "Remove " + a.name);
      x.addEventListener("click", function () { S.attachments.splice(i, 1); renderAttachments(); });
      c.appendChild(x);
      els.attachments.appendChild(c);
    });
  }

  // ---------- slash menu ----------
  var slashSel = 0, slashItems = [];
  function allCommands() {
    var out = [];
    S.commands.forEach(function (c) { out.push(c); if (c.alias) out.push({ name: c.alias, desc: c.desc + " (alias of " + c.name + ")" }); });
    return out;
  }
  function updateSlash() {
    var v = els.input.value;
    if (!/^\/[^\s/]*$/.test(v)) { hideSlash(); return; }
    slashItems = allCommands().filter(function (c) { return c.name.indexOf(v) === 0; });
    if (!slashItems.length) { hideSlash(); return; }
    slashSel = Math.min(slashSel, slashItems.length - 1);
    els.slash.textContent = "";
    slashItems.forEach(function (c, i) {
      var it = el("div", "item"); it.setAttribute("role", "option"); it.id = "slash-" + i;
      it.setAttribute("aria-selected", i === slashSel ? "true" : "false");
      it.appendChild(el("span", "k", c.name)); it.appendChild(el("span", "d", c.desc));
      it.addEventListener("mousedown", function (e) { e.preventDefault(); pickSlash(i); });
      els.slash.appendChild(it);
    });
    els.slash.hidden = false;
    els.input.setAttribute("aria-activedescendant", "slash-" + slashSel);
    els.input.setAttribute("aria-expanded", "true");
  }
  function hideSlash() { els.slash.hidden = true; slashItems = []; els.input.removeAttribute("aria-activedescendant"); els.input.setAttribute("aria-expanded", "false"); }
  function pickSlash(i) {
    var c = slashItems[i]; if (!c) return;
    var needsArg = c.name === "/model" || c.name === "/save";
    hideSlash();
    if (needsArg) { els.input.value = c.name + " "; autosize(); els.input.focus(); }
    else { els.input.value = ""; autosize(); runCommand(c.name); }
  }

  // ---------- command palette ----------
  var palSel = 0, palItems = [];
  function paletteActions() {
    var acts = [
      { k: "New chat", d: "Start a fresh conversation", run: newChat, key: "Ctrl+Shift+O" },
      { k: "Switch model…", d: "Focus the model picker", run: function () { els.model.focus(); els.model.select(); }, key: "Ctrl+Shift+M" },
      { k: "Search conversations", d: "Open a saved conversation", run: function () { openSidebar(); els.search.focus(); }, key: "Ctrl+Shift+F" },
      { k: "Stop", d: "Interrupt the running turn", run: stop, key: "Esc" },
      { k: "Regenerate", d: "Re-run the latest message", run: regenerate },
      { k: "Edit last message", d: "Edit and resend the latest message", run: editLast, key: "↑ in empty box" },
      { k: "Toggle theme", d: "System / light / dark", run: cycleTheme },
      { k: "Toggle sidebar", d: "Show or hide conversations", run: toggleSidebar, key: "Ctrl+Shift+S" },
      { k: "Project: AGENTS.md & memory", d: "Show this project's AGENTS.md and memory entries", run: openProject },
      { k: "Keyboard shortcuts", d: "Show commands and shortcuts", run: openHelp, key: "?" },
      { k: "Attach files", d: "Attach images or text files", run: function () { els.file.click(); } },
    ];
    allCommands().forEach(function (c) {
      acts.push({ k: c.name, d: c.desc, run: function () {
        if (c.name === "/model" || c.name === "/save") { els.input.value = c.name + " "; autosize(); els.input.focus(); } else runCommand(c.name);
      } });
    });
    (S.state && S.state.models || []).slice(0, 60).forEach(function (m) { acts.push({ k: "Model: " + m, d: "Switch to " + m, run: function () { switchModel(m); } }); });
    return acts;
  }
  function openPalette() {
    closeDialogs();
    els.paletteInput.value = ""; palSel = 0; renderPalette();
    els.palette.showModal(); els.paletteInput.focus();
  }
  function renderPalette() {
    var q = els.paletteInput.value.toLowerCase().trim();
    palItems = paletteActions().filter(function (a) { return !q || (a.k + " " + a.d).toLowerCase().indexOf(q) >= 0; });
    palSel = Math.max(0, Math.min(palSel, palItems.length - 1));
    els.paletteList.textContent = "";
    palItems.forEach(function (a, i) {
      var it = el("li", "item"); it.setAttribute("role", "option"); it.id = "pal-" + i;
      it.setAttribute("aria-selected", i === palSel ? "true" : "false");
      it.appendChild(el("span", "k", a.k)); it.appendChild(el("span", "d", a.d + (a.key ? " · " + a.key : "")));
      it.addEventListener("click", function () { runPalette(i); });
      els.paletteList.appendChild(it);
    });
    els.paletteInput.setAttribute("aria-activedescendant", "pal-" + palSel);
    var s = els.paletteList.children[palSel]; if (s) s.scrollIntoView({ block: "nearest" });
  }
  function runPalette(i) { var a = palItems[i]; els.palette.close(); if (a) a.run(); }

  // ---------- dialogs ----------
  var SHORTCUTS = [
    ["Enter", "Send"], ["Shift+Enter", "New line"], ["/", "Command menu (in the message box)"], ["Ctrl/⌘+K", "Command palette"],
    ["?", "This help (outside the message box)"], ["Esc", "Stop the running turn / close menus"], ["↑ (empty box)", "Edit the last message"],
    ["Ctrl/⌘+Shift+O", "New chat"], ["Ctrl/⌘+Shift+M", "Model picker"], ["Ctrl/⌘+Shift+F", "Search conversations"],
    ["Ctrl/⌘+Shift+S", "Toggle the sidebar"], ["Ctrl/⌘+Shift+C", "Copy the last reply"], ["/", "Focus the message box (outside it)"],
  ];
  function openHelp() {
    closeDialogs();
    els.helpBody.textContent = "";
    els.helpBody.appendChild(el("h3", null, "Slash commands"));
    var g = el("div", "keys");
    allCommands().forEach(function (c) { g.appendChild(el("code", null, c.name)); g.appendChild(el("span", null, c.desc)); });
    g.appendChild(el("code", null, "//text")); g.appendChild(el("span", null, "send a message that starts with /"));
    els.helpBody.appendChild(g);
    els.helpBody.appendChild(el("h3", null, "Keyboard shortcuts"));
    var k = el("div", "keys");
    SHORTCUTS.forEach(function (s) { k.appendChild(el("kbd", null, s[0])); k.appendChild(el("span", null, s[1])); });
    els.helpBody.appendChild(k);
    els.help.showModal();
  }
  function openProject() {
    closeDialogs();
    var st = S.state || {}, b = els.projectBody;
    b.textContent = "";
    b.appendChild(el("h3", null, "AGENTS.md"));
    var ag = st.agents_md || {};
    if (!ag.enabled) b.appendChild(el("p", null, "AGENTS.md is off for this session (--no-agents-md)."));
    else if (!ag.text) b.appendChild(el("p", null, "No ./AGENTS.md in " + st.cwd + ". Create one to give the agent project instructions."));
    else { var d = el("div", "agents md"); setMD(d, ag.text); b.appendChild(d); }
    b.appendChild(el("h3", null, "Memory"));
    var mem = st.memory || {};
    if (!mem.enabled) b.appendChild(el("p", null, "Memory is off. Start with --memory to remember facts across sessions."));
    else {
      b.appendChild(el("p", null, "Store: " + (mem.path || "?") + " · the agent reads and writes it with its memory tools; manage it with tinyagent memory."));
      var ul = el("ul");
      (mem.entries || []).forEach(function (e) { ul.appendChild(el("li", null, "[" + e.status + "] " + e.text)); });
      if (!(mem.entries || []).length) ul.appendChild(el("li", null, "(no entries yet)"));
      b.appendChild(ul);
    }
    els.project.showModal();
  }
  function closeDialogs() { [els.palette, els.help, els.project].forEach(function (d) { if (d.open) d.close(); }); }

  // ---------- sidebar ----------
  function refreshHistory() {
    return api("/api/history").then(function (h) { S.history = h || []; renderSessions(); }).catch(function () {});
  }
  function renderSessions() {
    var q = els.search.value.toLowerCase().trim();
    els.sessions.textContent = "";
    var items = S.history.filter(function (h) { return !q || (h.task + " " + h.name + " " + h.model).toLowerCase().indexOf(q) >= 0; });
    if (!items.length) { els.sessions.appendChild(el("li", "m", q ? "No matches." : "No saved conversations yet.")); return; }
    items.forEach(function (h) {
      var li = el("li"); li.tabIndex = 0; li.setAttribute("role", "option"); li.dataset.name = h.name;
      if (h.current) { li.className = "current"; li.setAttribute("aria-current", "true"); }
      li.appendChild(el("span", "t", h.task || h.name));
      var when = new Date(h.modified);
      li.appendChild(el("span", "m", when.toLocaleString() + " · " + (h.status || "?") + (h.cost ? " · $" + h.cost.toFixed(3) : "")));
      li.title = h.name + (h.model ? " · " + h.model : "");
      els.sessions.appendChild(li);
    });
  }
  function openSession(name) {
    if (!name) return;
    api("/api/history/open", { name: name }).then(function (r) { notice("continuing " + name + " · new checkpoint " + r.checkpoint); if (matchMedia("(max-width: 760px)").matches) els.app.classList.remove("open-mobile"); els.input.focus(); }).catch(fail);
  }
  function toggleSidebar() {
    if (matchMedia("(max-width: 760px)").matches) els.app.classList.toggle("open-mobile");
    else els.app.classList.toggle("collapsed");
    $("toggle-sidebar").setAttribute("aria-expanded", String(!els.app.classList.contains("collapsed")));
  }
  function openSidebar() { els.app.classList.remove("collapsed"); if (matchMedia("(max-width: 760px)").matches) els.app.classList.add("open-mobile"); }

  // ---------- edit / regenerate ----------
  function regenerate() {
    if (!canRegen()) { notice("Nothing to regenerate yet (only the latest turn of this session can be regenerated).", "error"); return; }
    api("/api/regenerate", {}).catch(fail);
  }
  function editLast() {
    var users = els.msgs.querySelectorAll(".msg.user");
    var u = users[users.length - 1];
    if (!u || !canRegen()) return false;
    var body = u.querySelector(".body");
    if (u.querySelector("textarea")) return true;
    var ta = el("textarea", "input"); ta.value = body.dataset.src; ta.rows = Math.min(12, ta.value.split("\n").length + 1);
    ta.setAttribute("aria-label", "Edit message");
    var row = el("div", "row");
    var ok = el("button", "btn primary small", "Resend"); ok.type = "button";
    var cancel = el("button", "btn small", "Cancel"); cancel.type = "button";
    row.appendChild(ok); row.appendChild(cancel);
    body.hidden = true; u.appendChild(ta); u.appendChild(row); ta.focus();
    function done() { ta.remove(); row.remove(); body.hidden = false; }
    ok.addEventListener("click", function () { var t = ta.value; done(); api("/api/regenerate", { text: t }).catch(fail); });
    cancel.addEventListener("click", function () { done(); els.input.focus(); });
    ta.addEventListener("keydown", function (e) {
      if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); ok.click(); }
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); cancel.click(); }
    });
    return true;
  }

  // ---------- theme ----------
  var THEMES = ["system", "light", "dark"];
  function applyTheme(t) {
    if (t === "system") document.documentElement.removeAttribute("data-theme"); else document.documentElement.setAttribute("data-theme", t);
    $("theme-btn").setAttribute("aria-label", "Theme: " + t); $("theme-btn").title = "Theme: " + t + " (click to change)";
    localStorage.setItem("tinyagent-theme", t);
  }
  function cycleTheme() { var t = localStorage.getItem("tinyagent-theme") || "system"; applyTheme(THEMES[(THEMES.indexOf(t) + 1) % 3]); }

  // ---------- copy ----------
  function copy(text, btn) {
    var doneFn = function () { if (btn) { var o = btn.textContent; btn.textContent = "Copied"; setTimeout(function () { btn.textContent = o; }, 1200); } };
    if (navigator.clipboard && window.isSecureContext) navigator.clipboard.writeText(text).then(doneFn, function () { fallbackCopy(text); doneFn(); });
    else { fallbackCopy(text); doneFn(); }
  }
  function fallbackCopy(text) {
    var t = el("textarea"); t.value = text; document.body.appendChild(t); t.select();
    try { document.execCommand("copy"); } catch (e) { /* ignore */ }
    t.remove();
  }
  function msgText(m) {
    var parts = [];
    m.querySelectorAll(".md, .body").forEach(function (n) { parts.push(n.dataset.src || n.textContent); });
    return parts.join("\n\n");
  }

  // ---------- composer ----------
  function autosize() { els.input.style.height = "auto"; els.input.style.height = Math.min(els.input.scrollHeight, window.innerHeight * 0.4) + "px"; }

  els.form.addEventListener("submit", function (e) { e.preventDefault(); sendText(els.input.value); });
  els.input.addEventListener("input", function () { autosize(); slashSel = 0; updateSlash(); });
  els.input.addEventListener("keydown", function (e) {
    if (!els.slash.hidden && slashItems.length) {
      if (e.key === "ArrowDown") { e.preventDefault(); slashSel = (slashSel + 1) % slashItems.length; updateSlash(); return; }
      if (e.key === "ArrowUp") { e.preventDefault(); slashSel = (slashSel - 1 + slashItems.length) % slashItems.length; updateSlash(); return; }
      if (e.key === "Tab" || (e.key === "Enter" && !e.shiftKey)) { e.preventDefault(); pickSlash(slashSel); return; }
      if (e.key === "Escape") { e.preventDefault(); hideSlash(); return; }
    }
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); if (!S.running) sendText(els.input.value); return; }
    if (e.key === "ArrowUp" && els.input.value === "") { if (editLast()) e.preventDefault(); }
  });
  els.input.addEventListener("blur", function () { setTimeout(hideSlash, 150); });
  els.input.addEventListener("paste", function (e) {
    var files = Array.prototype.filter.call((e.clipboardData || {}).files || [], function (f) { return f.type.indexOf("image/") === 0; });
    if (files.length) { e.preventDefault(); addFiles(files); }
  });
  els.file.addEventListener("change", function () { addFiles(els.file.files); els.file.value = ""; });
  var dragDepth = 0;
  window.addEventListener("dragenter", function (e) { if (e.dataTransfer && Array.prototype.indexOf.call(e.dataTransfer.types, "Files") >= 0) { dragDepth++; els.drop.hidden = false; } });
  window.addEventListener("dragleave", function () { if (--dragDepth <= 0) { dragDepth = 0; els.drop.hidden = true; } });
  window.addEventListener("dragover", function (e) { e.preventDefault(); });
  window.addEventListener("drop", function (e) { e.preventDefault(); dragDepth = 0; els.drop.hidden = true; if (e.dataTransfer && e.dataTransfer.files.length) addFiles(e.dataTransfer.files); });

  els.send.addEventListener("click", function () {});
  els.stop.addEventListener("click", stop);
  $("new-chat").addEventListener("click", newChat);
  $("model-apply").addEventListener("click", function () { switchModel(els.model.value); });
  els.model.addEventListener("keydown", function (e) { if (e.key === "Enter") { e.preventDefault(); switchModel(els.model.value); els.input.focus(); } if (e.key === "Escape") { els.model.value = S.state ? S.state.model : ""; els.input.focus(); } });
  els.model.addEventListener("change", function () { if (S.state && els.model.value !== S.state.model && (S.state.models || []).indexOf(els.model.value) >= 0) switchModel(els.model.value); });
  $("palette-btn").addEventListener("click", openPalette);
  $("help-btn").addEventListener("click", openHelp);
  $("theme-btn").addEventListener("click", cycleTheme);
  $("project-btn").addEventListener("click", openProject);
  $("toggle-sidebar").addEventListener("click", toggleSidebar);
  els.jump.addEventListener("click", function () { S.pinned = true; scroll(); els.input.focus(); });
  els.log.addEventListener("scroll", function () { S.pinned = nearBottom(); updateJump(); });
  els.search.addEventListener("input", renderSessions);
  els.search.addEventListener("keydown", function (e) {
    if (e.key === "ArrowDown") { e.preventDefault(); var f = els.sessions.querySelector("li[data-name]"); if (f) f.focus(); }
    if (e.key === "Enter") { var g = els.sessions.querySelector("li[data-name]"); if (g) openSession(g.dataset.name); }
  });
  els.sessions.addEventListener("click", function (e) { var li = e.target.closest("li[data-name]"); if (li) openSession(li.dataset.name); });
  els.sessions.addEventListener("keydown", function (e) {
    var li = e.target.closest("li[data-name]"); if (!li) return;
    if (e.key === "Enter" || e.key === " ") { e.preventDefault(); openSession(li.dataset.name); }
    if (e.key === "ArrowDown" && li.nextElementSibling) { e.preventDefault(); li.nextElementSibling.focus(); }
    if (e.key === "ArrowUp") { e.preventDefault(); (li.previousElementSibling || els.search).focus(); }
  });
  els.paletteInput.addEventListener("input", function () { palSel = 0; renderPalette(); });
  els.paletteInput.addEventListener("keydown", function (e) {
    if (e.key === "ArrowDown") { e.preventDefault(); palSel = (palSel + 1) % Math.max(1, palItems.length); renderPalette(); }
    if (e.key === "ArrowUp") { e.preventDefault(); palSel = (palSel - 1 + palItems.length) % Math.max(1, palItems.length); renderPalette(); }
    if (e.key === "Enter") { e.preventDefault(); runPalette(palSel); }
  });
  els.palette.addEventListener("close", function () { els.input.focus(); });
  els.msgs.addEventListener("click", function (e) {
    var b = e.target.closest("button"); if (!b) return;
    if (b.hasAttribute("data-copy")) { copy(b.closest(".code").querySelector("code").textContent, b); return; }
    var act = b.dataset.act;
    if (act === "copy-msg") copy(msgText(b.closest(".msg")), b);
    else if (act === "regen") regenerate();
    else if (act === "edit") editLast();
  });

  document.addEventListener("keydown", function (e) {
    var mod = e.metaKey || e.ctrlKey;
    var typing = /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement.tagName);
    var k = e.key.toLowerCase();
    if (mod && k === "k") { e.preventDefault(); if (els.palette.open) els.palette.close(); else openPalette(); return; }
    if (mod && e.shiftKey && k === "o") { e.preventDefault(); newChat(); return; }
    if (mod && e.shiftKey && k === "m") { e.preventDefault(); els.model.focus(); els.model.select(); return; }
    if (mod && e.shiftKey && k === "f") { e.preventDefault(); openSidebar(); els.search.focus(); return; }
    if (mod && e.shiftKey && k === "s") { e.preventDefault(); toggleSidebar(); return; }
    if (mod && e.shiftKey && k === "c") {
      var as = els.msgs.querySelectorAll(".msg.assistant"); if (as.length) { e.preventDefault(); copy(msgText(as[as.length - 1])); notice("Copied the last reply."); } return;
    }
    if (e.key === "Escape" && !els.palette.open && !els.help.open && !els.project.open && els.slash.hidden && S.running) { e.preventDefault(); stop(); return; }
    if (!typing && !mod && e.key === "?") { e.preventDefault(); openHelp(); return; }
    if (!typing && !mod && e.key === "/" && !document.querySelector("dialog[open]")) { e.preventDefault(); els.input.focus(); els.input.value = "/"; updateSlash(); }
  });

  // ---------- boot ----------
  applyTheme(localStorage.getItem("tinyagent-theme") || "system");
  if (matchMedia("(max-width: 760px)").matches) $("toggle-sidebar").setAttribute("aria-expanded", "false");
  if (!token) { banner("Missing token: open the full URL that tinyagent web printed (it ends in ?token=…).", "error"); return; }
  api("/api/commands").then(function (c) { S.commands = c; }).catch(function (e) {
    if (e.status === 401) banner("This page's token is not valid for the running server. Open the URL tinyagent web printed.", "error");
  });
  refreshHistory();
  connect();
  els.input.focus();
  window.TinyAgentUI = { state: S }; // for debugging from the console
})();
