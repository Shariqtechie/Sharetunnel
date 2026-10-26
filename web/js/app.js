"use strict";
/* ShareTunnel admin panel. Plain JavaScript, no build step, no external requests. */

const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => [...r.querySelectorAll(s)];
const NS = "http://www.w3.org/2000/svg";

// tiny DOM helpers (everything is built with textContent, so file names can never inject HTML)
function icon(name, cls = "i") {
  const s = document.createElementNS(NS, "svg"), u = document.createElementNS(NS, "use");
  s.setAttribute("class", cls); s.setAttribute("aria-hidden", "true"); u.setAttribute("href", "#i-" + name); s.append(u); return s;
}
function h(tag, props = {}, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === "class") e.className = v; else if (k.startsWith("on")) e[k] = v; else if (v !== false && v != null) e.setAttribute(k, v === true ? "" : v);
  }
  for (const c of kids.flat()) if (c != null && c !== false) e.append(c.nodeType ? c : document.createTextNode(c));
  return e;
}

// ---------- state ----------
const S = { status: "", url: "", since: 0, logs: [], version: "" };
let shares = [], sharesErr = "", sharesLoaded = false, upd = {}, doctor = null, doctorBusy = false, formDirty = false;
let filter = "all", sortBy = "new", query = "", logErrOnly = false;
let tick = 0, lastUrl = null, tableKey = "", logKey = "", toggleKey = "";

const KIND = { folder: { icon: "folder", label: "Folder" }, video: { icon: "film", label: "Video" }, image: { icon: "image", label: "Image" }, audio: { icon: "music", label: "Audio" }, file: { icon: "file", label: "File" } };
const TITLES = { dashboard: "Dashboard", shares: "Shared links", logs: "Activity log", setup: "Setup", updates: "Updates" };
const STATUS = { running: "Online", starting: "Connecting", stopped: "Stopped", error: "Error" };

// ---------- formatting ----------
function bytes(n) { if (n < 1024) return n + " B"; let u = 0, v = n; while (v >= 1024 && u < 4) { v /= 1024; u++; } return v.toFixed(1) + " " + ["B", "KB", "MB", "GB", "TB"][u]; }
function dur(sec) { sec = Math.max(0, sec | 0); const d = Math.floor(sec / 86400), hh = Math.floor(sec % 86400 / 3600), m = Math.floor(sec % 3600 / 60); return d ? d + "d " + hh + "h" : hh ? hh + "h " + m + "m" : m ? m + " min" : sec + "s"; }
function rel(ts) { if (!ts) return "–"; const s = Math.floor(Date.now() / 1000) - ts; if (s < 60) return "just now"; if (s < 3600) return Math.floor(s / 60) + " min ago"; if (s < 86400) return Math.floor(s / 3600) + " h ago"; const d = Math.floor(s / 86400); return d === 1 ? "yesterday" : d + " days ago"; }
function expiry(iso) {
  const t = Date.parse(iso); if (isNaN(t)) return { text: iso, cls: "" };
  const d = Math.ceil((t - Date.now()) / 864e5);
  if (d < 0) return { text: "Expired", cls: "bad" }; if (d === 0) return { text: "Expires today", cls: "warn" }; if (d === 1) return { text: "Expires tomorrow", cls: "warn" };
  return { text: "Expires in " + d + " days", cls: d <= 3 ? "warn" : "" };
}
const ver = v => /^\d/.test(v || "") ? "v" + v : (v || "–");

// ---------- api / ui helpers ----------
async function api(path, body) {
  const r = await fetch(path, { method: body === undefined ? "GET" : "POST", headers: { "Content-Type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body) });
  if (r.status === 401 && path !== "/api/login") { showLogin(); throw new Error("login"); }
  return r.json();
}
function toast(msg, kind = "") {
  const t = h("div", { class: "toast " + kind, role: "status" }, icon(kind === "bad" ? "alert" : "check"), msg); $("#toasts").append(t);
  setTimeout(() => { t.classList.add("out"); setTimeout(() => t.remove(), 300); }, 2600);
}
function copy(text, btn) {
  const done = () => { toast("Copied to clipboard"); if (!btn) return; const o = [...btn.childNodes]; btn.classList.add("done"); btn.replaceChildren(icon("check"), "Copied"); setTimeout(() => { btn.classList.remove("done"); btn.replaceChildren(...o); }, 1400); };
  if (navigator.clipboard && window.isSecureContext) { navigator.clipboard.writeText(text).then(done); return; }
  const t = h("textarea"); t.value = text; t.style.cssText = "position:fixed;opacity:0"; document.body.append(t); t.select();   // plain-http fallback
  try { document.execCommand("copy"); done(); } catch (e) { prompt("Copy this link:", text); } t.remove();
}
function ask(title, body, ok) {
  return new Promise(res => {
    const d = $("#confirm"); $("#cfTitle").textContent = title; $("#cfBody").textContent = body; $("#cfYes").textContent = ok;
    const end = v => { d.close(); res(v); };
    $("#cfYes").onclick = () => end(true); $("#cfNo").onclick = () => end(false); d.oncancel = () => res(false); d.showModal();
  });
}

// ---------- theme / routing / login ----------
function paintTheme() { const dark = document.documentElement.dataset.theme === "dark"; $("#themeBtn use").setAttribute("href", dark ? "#i-sun" : "#i-moon"); $("#themeBtn").setAttribute("aria-label", dark ? "Switch to light theme" : "Switch to dark theme"); }
function route() {
  const v = location.hash.replace(/^#\/?/, "") || "dashboard", view = TITLES[v] ? v : "dashboard";
  $$(".view").forEach(s => s.classList.toggle("hidden", s.id !== "view-" + view));
  $$(".nav").forEach(a => a.classList.toggle("on", a.dataset.view === view));
  $("#pageTitle").textContent = TITLES[view]; $("#app").classList.remove("nav-open");
  if (view === "logs") requestAnimationFrame(() => { const b = $("#logs"); b.scrollTop = b.scrollHeight; });
}
let authMode = "file"; // env | file | setup, from /api/session
function applyLoginMode(s) {
  authMode = s.mode || authMode;
  const setup = authMode === "setup";
  $("#signinBox").classList.toggle("hidden", setup);
  $("#setupBox").classList.toggle("hidden", !(setup && s.can_setup));
  $("#setupBlocked").classList.toggle("hidden", !(setup && !s.can_setup));
  $("#forgotEnv").classList.toggle("hidden", authMode !== "env");
  $("#forgotFile").classList.toggle("hidden", authMode === "env");
  setTimeout(() => (setup ? $("#npw1") : $("#pw")).focus(), 50);
}
async function showLogin() {
  $("#app").classList.add("hidden"); $("#login").classList.remove("hidden");
  try { applyLoginMode(await api("/api/session")); } catch (e) { /* offline: keep the sign-in form */ }
}
function loginDone() {
  ["#loginErr", "#setupErr"].forEach(id => $(id).textContent = "");
  ["#pw", "#npw1", "#npw2"].forEach(id => $(id).value = ""); $("#forgot").classList.add("hidden");
  tick = 0; doctor = null; showApp(); poll();
}
function showApp() { $("#login").classList.add("hidden"); $("#app").classList.remove("hidden"); route(); renderPwCard(); }

// ---------- render: tunnel, stats, health ----------
function renderStatus() {
  const on = S.status !== "stopped", running = S.status === "running";
  $("#verChip").textContent = ver(S.version);
  $("#pill").className = "pill " + S.status; $("#pillText").textContent = STATUS[S.status] || "…";
  const key = on + "|" + S.status;
  if (key !== toggleKey) { toggleKey = key; const b = $("#toggle"); b.className = on ? "btn danger" : "btn"; b.setAttribute("aria-label", on ? "Stop tunnel" : "Start tunnel"); b.replaceChildren(icon(on ? "stop" : "play"), h("span", {}, on ? "Stop tunnel" : "Start tunnel")); }
  const u = $("#heroUrl"); u.textContent = S.url || "Not running"; u.classList.toggle("live", !!S.url);
  if (S.url && S.url !== lastUrl) { u.classList.remove("flash"); void u.offsetWidth; u.classList.add("flash"); }
  $("#heroSub").textContent = running ? "Online for " + dur(Date.now() / 1000 - S.since) + ". Your share links use this address." : S.status === "starting" ? "Connecting to Cloudflare…" : S.status === "error" ? (S.problem || "Something went wrong. See the activity log.") : (doctor && !doctor.ready ? "The tunnel is off. Fix the issue on the Setup page first, then press Start tunnel." : "The tunnel is off. Press Start tunnel to get an address anyone can open.");
  $("#heroCopy").disabled = !S.url; $("#heroCopy").onclick = e => copy(S.url, e.currentTarget);
  const o = $("#heroOpen"); o.classList.toggle("disabled", !S.url); if (S.url) o.href = S.url; else o.removeAttribute("href");
}
function setStat(id, v, sub, cls = "") { $("#" + id).textContent = v; const s = $("#" + id + "S"); s.textContent = sub || ""; s.className = "stat-s " + cls; }
function renderStats() {
  const running = S.status === "running";
  setStat("stTunnel", STATUS[S.status] || "–", running ? "Up for " + dur(Date.now() / 1000 - S.since) : S.status === "stopped" ? "Not running" : "", running ? "ok" : "");
  const pw = shares.filter(s => s.has_pw).length;
  setStat("stLinks", sharesLoaded ? shares.length : "–", sharesLoaded ? pw + " password-protected" : "");
  setStat("stSize", sharesLoaded ? bytes(shares.reduce((a, s) => a + s.size, 0)) : "–", sharesLoaded ? "across " + shares.length + " item" + (shares.length === 1 ? "" : "s") : "");
  setStat("stVer", ver(S.version), upd.available ? "Update available" : upd.dev ? "Development build" : upd.checked_at && !upd.error ? "Up to date" : "", upd.available ? "warn" : "ok");
  $("#navCount").textContent = sharesLoaded && shares.length ? shares.length : "";
  $("#navUpdate").classList.toggle("hidden", !upd.available);
}
function renderHealth() {
  const st = S.status, short = t => t.length > 70 ? t.slice(0, 70) + "…" : t;
  const items = [
    { s: st === "running" ? "ok" : st === "starting" ? "warn" : st === "error" ? "bad" : "idle", t: "Cloudflare tunnel", d: st === "running" ? "Connected" : st === "starting" ? "Connecting…" : st === "error" ? (S.problem ? "Not started, see Setup" : "Failed to register address") : "Stopped" },
    { s: st === "running" ? "ok" : st === "error" ? "bad" : "idle", t: "Nextcloud access", d: st === "running" ? "Address added to trusted domains" : st === "error" ? short(S.problem || "Could not update trusted domains") : "Waiting for the tunnel" },
    { s: sharesErr ? "bad" : sharesLoaded ? "ok" : "idle", t: "Share database", d: sharesErr ? short(sharesErr) : sharesLoaded ? "Read-only access OK" : "Checking…" },
    { s: upd.error ? "warn" : upd.available ? "info" : upd.checked_at ? "ok" : "idle", t: "Software updates", d: upd.error ? short(upd.error) : upd.available ? upd.latest + " is available" : upd.dev ? "Development build" : upd.checked_at ? "Up to date" : "Not checked yet" },
  ];
  $("#health").replaceChildren(...items.map(i => h("li", { class: i.s }, h("span", { class: "hi" }, icon(i.s === "ok" ? "check" : i.s === "idle" ? "clock" : i.s === "info" ? "download" : "alert")), h("div", {}, h("b", {}, i.t), h("small", {}, i.d)))));
}

// ---------- render: shares ----------
function emptyState(none) {
  return h("div", { class: "empty" }, h("div", { class: "eic" }, icon(none ? "link" : "search")), h("h4", {}, none ? "No public links yet" : "No matches"),
    h("p", {}, none ? "In Nextcloud, click Share on any file or folder and choose a share link. It shows up here automatically." : "Try a different search or filter."));
}
function shareRow(s, n) {
  const k = KIND[s.kind] || KIND.file, ex = s.expiration ? expiry(s.expiration) : null, link = S.url ? S.url + "/s/" + s.token : "";
  return h("tr", { style: false, class: "", "data-n": n },
    h("td", { class: "c-name" }, h("div", { class: "file" }, h("span", { class: "ficon k-" + s.kind }, icon(k.icon)), h("div", {}, h("div", { class: "fname" }, s.file_target.replace(/^\//, "")), h("div", { class: "muted sm" }, "by " + s.owner)))),
    h("td", { class: "c-type" }, h("span", { class: "badge" }, k.label)),
    h("td", { class: "c-size num" }, s.size_text),
    h("td", { class: "c-prot" }, h("div", { class: "badges" }, s.has_pw ? h("span", { class: "badge ok" }, icon("lock"), "Password") : h("span", { class: "badge" }, "No password"), ex && h("span", { class: "badge " + ex.cls }, icon("clock"), ex.text))),
    h("td", { class: "c-date muted" }, rel(s.created)),
    h("td", { class: "c-act" }, h("div", { class: "row" },
      h("button", { class: "btn sm secondary", disabled: !S.url, title: S.url ? "" : "Start the tunnel first", onclick: e => copy(link, e.currentTarget) }, icon("copy"), h("span", { class: "lbl" }, "Copy link")),
      S.url && h("a", { class: "icon-btn", href: link, target: "_blank", rel: "noopener", "aria-label": "Open link in a new tab" }, icon("ext")))));
}
function table(list) {
  const t = h("table", {}, h("thead", {}, h("tr", {}, h("th", {}, "Name"), h("th", { class: "c-type" }, "Type"), h("th", {}, "Size"), h("th", { class: "c-prot" }, "Protection"), h("th", { class: "c-date" }, "Shared"), h("th", {}, ""))), h("tbody", {}));
  list.forEach((s, i) => { const r = shareRow(s, i); r.style.setProperty("--n", i); t.tBodies[0].append(r); }); return t;
}
function visibleShares() {
  const a = shares.filter(s => (filter === "all" || s.kind === filter) && (!query || (s.file_target + " " + s.owner).toLowerCase().includes(query)));
  return a.sort(sortBy === "name" ? (x, y) => x.file_target.localeCompare(y.file_target) : sortBy === "size" ? (x, y) => y.size - x.size : (x, y) => y.created - x.created || y.id - x.id);
}
function renderTables() {
  if (!sharesLoaded) return;
  const key = JSON.stringify([S.url, shares, filter, sortBy, query]); if (key === tableKey) return; tableKey = key;
  const list = visibleShares(); $("#shareErr").textContent = sharesErr ? "Could not read shares: " + sharesErr : "";
  $("#sharesBox").replaceChildren(list.length ? table(list) : emptyState(!shares.length));
  const recent = [...shares].sort((x, y) => y.created - x.created || y.id - x.id).slice(0, 5);
  $("#recentBox").replaceChildren(recent.length ? table(recent) : emptyState(true));
}
async function loadShares() {
  const d = await api("/api/shares"); shares = d.shares || []; sharesErr = d.error || ""; sharesLoaded = true; tableKey = ""; renderTables(); renderStats(); renderHealth();
}

// ---------- render: logs ----------
function renderLogs() {
  const lines = S.logs.filter(l => !logErrOnly || /ERR|ERROR|failed|ROLLING|WRN/.test(l)), key = logErrOnly + "|" + lines.join("\n"); if (key === logKey) return; logKey = key;
  const box = $("#logs"), bottom = box.scrollTop + box.clientHeight >= box.scrollHeight - 30;
  box.replaceChildren(...lines.map(l => {
    const m = l.match(/^(\d\d:\d\d:\d\d) (.*)$/), cls = /ERR|ERROR|failed|ROLLING/.test(l) ? "err" : /WRN/.test(l) ? "warn" : /is up|trusts|UPDATE OK|rollback done/.test(l) ? "ok" : "";
    return h("div", { class: "ln " + cls }, h("time", {}, m ? m[1] : ""), h("span", {}, m ? m[2] : l));
  }));
  if (!lines.length) box.append(h("div", { class: "muted" }, "Nothing logged yet."));
  if (bottom) box.scrollTop = box.scrollHeight;
}

// ---------- render: updates ----------
const clean = t => t.replace(/\*\*|__|`/g, "").replace(/\[([^\]]+)\]\([^)]*\)/g, "$1");
function renderNotes(md) {
  const box = $("#notes"); box.replaceChildren(); let ul = null;
  for (const raw of (md || "").split("\n")) {
    const line = raw.trim(); if (!line) { ul = null; continue; }
    const li = line.match(/^[*-]\s+(.*)/), hd = line.match(/^#{1,6}\s+(.*)/);
    if (li) { if (!ul) { ul = h("ul"); box.append(ul); } ul.append(h("li", {}, clean(li[1]))); continue; }
    ul = null; box.append(hd ? h("h4", {}, clean(hd[1])) : h("p", {}, clean(line)));
  }
  if (!box.children.length) box.append(h("p", {}, md === undefined ? "Check for updates to see the latest release notes." : "No release notes for this version."));
}
function renderUpdate() {
  const u = upd, ic = $("#updIcon");
  ic.className = "upd-icon" + (u.available ? " avail" : u.error ? " bad" : ""); ic.replaceChildren(icon(u.available ? "download" : u.error ? "alert" : "check"));
  $("#updTitle").textContent = u.available ? "Version " + u.latest + " is available" : u.error ? "Couldn't check for updates" : u.dev ? "Development build" : u.checked_at ? "You're up to date" : "Not checked yet";
  $("#updSub").textContent = u.available ? "You have " + ver(u.current) + ". Your tunnel is back about 30 seconds after updating." : u.error ? u.error : u.dev ? "Update checks apply to official releases." : u.checked_at ? ver(u.current) + " is the latest version. Checked " + rel(u.checked_at) + "." : "Press the button to look for a new version.";
  const mg = !!u.managed;
  if (mg && u.available) $("#updSub").textContent = "You have " + ver(u.current) + ". Update from CasaOS for the best security and newest features (see below).";
  $("#applyBtn").classList.toggle("hidden", !u.available || mg); $("#prefsCard").classList.toggle("hidden", mg);
  $("#managedCard").classList.toggle("hidden", !mg); $("#managedCard").classList.toggle("avail", mg && !!u.available); $("#updGrid").classList.toggle("single", mg);
  const how = "open this app's menu in CasaOS (the three dots on its icon) and choose Update. Your settings and shared links are kept.";
  $("#managedText").textContent = u.available ? "Version " + u.latest + " is out. For the best security and newest features, update now from CasaOS: " + how : "ShareTunnel is installed through CasaOS, so CasaOS handles updates, and a new version will show up here. To update for the best security and newest features, " + how;
  const rl = $("#relLink"); rl.classList.toggle("hidden", !u.url); if (u.url) rl.href = u.url;
  renderNotes(u.latest ? u.notes : undefined);
  $$("#modes input").forEach(r => r.checked = r.value === u.mode); $("#hourBox").classList.toggle("hidden", u.mode !== "auto"); if (u.hour != null) $("#hour").value = u.hour;
  renderStats(); renderHealth();
}
async function loadUpdate() { upd = await api("/api/update"); renderUpdate(); }
async function checkNow() {
  const b = $("#checkBtn"), old = [...b.childNodes]; b.disabled = true; b.replaceChildren(h("span", { class: "spin" }), "Checking…");
  try { upd = await api("/api/update/check", {}); renderUpdate(); toast(upd.error ? "Check failed" : upd.available ? "Update available: " + upd.latest : "You're up to date", upd.error ? "bad" : ""); }
  finally { b.disabled = false; b.replaceChildren(...old); }
}
async function applyNow() {
  if (!(await ask("Update to " + upd.latest + "?", "The tunnel restarts and this page reloads. If the new version fails to start, the old one is restored automatically.", "Update now"))) return;
  const r = await api("/api/update/apply", {}); if (r.error) { toast(r.error, "bad"); return; }
  const dlg = $("#progress"); dlg.oncancel = e => e.preventDefault(); dlg.showModal();
  const old = upd.current, t0 = Date.now(), timer = setInterval(async () => {
    try { const j = await (await fetch("/healthz", { cache: "no-store" })).json(); if (j.version && j.version !== old) { clearInterval(timer); $("#pgTitle").textContent = "Updated to " + ver(j.version); setTimeout(() => location.reload(), 900); } } catch (e) { /* swapping containers */ }
    if (Date.now() - t0 > 150000) { clearInterval(timer); $("#pgTitle").textContent = "Taking longer than expected"; $("#pgSub").textContent = "The old version may have been restored. Reload the page and check the activity log."; }
  }, 2000);
}
async function saveSettings() {
  const mode = ($("#modes input:checked") || {}).value || "notify"; upd = await api("/api/update/settings", { mode, hour: +$("#hour").value }); renderUpdate(); toast("Preferences saved");
}

// ---------- render: setup ----------
const CHK = { ok: "ok", warn: "warn", fail: "bad" };
function pickButtons(box, names, input) {
  box.replaceChildren(...(names || []).map(n => h("button", { class: "btn sm secondary", type: "button", onclick: () => { input.value = n; formDirty = true; } }, n)));
}
function renderSetup() {
  if (!doctor) return;
  const fails = doctor.checks.filter(c => c.status === "fail");
  $("#setupChecks").replaceChildren(...doctor.checks.map(c => h("li", { class: CHK[c.status] },
    h("span", { class: "hi" }, icon(c.status === "ok" ? "check" : "alert")),
    h("div", {}, h("b", {}, c.title), h("small", {}, c.detail), c.fix && c.status !== "ok" && h("small", { class: "fix" }, c.fix)))));
  const bn = $("#setupBanner"); bn.classList.toggle("hidden", !fails.length);
  if (fails.length) $("#setupBannerText").textContent = fails[0].title + ": " + fails[0].detail;
  const nb = $("#navSetup"); nb.classList.toggle("hidden", !fails.length); nb.classList.add("warn");
  if (!formDirty) { $("#cfgNC").value = doctor.config.nc_container; $("#cfgURL").value = doctor.config.nc_url; $("#cfgDB").value = doctor.config.db_container; }
  pickButtons($("#picksNC"), doctor.nextcloud_candidates, $("#cfgNC"));
  pickButtons($("#picksDB"), doctor.postgres_candidates, $("#cfgDB"));
  pickButtons($("#picksURL"), doctor.suggested_url && doctor.suggested_url !== $("#cfgURL").value ? [doctor.suggested_url] : [], $("#cfgURL"));
  $("#portNote").textContent = "Panel port: " + doctor.config.panel_port + ". To change it, set PANEL_PORT when you create the container (for example -e PANEL_PORT=9100) and open the panel on the new port.";
}
async function loadDoctor(fresh) {
  if (doctorBusy) return; doctorBusy = true;
  try { doctor = await api("/api/doctor" + (fresh ? "?fresh=1" : "")); renderSetup(); } catch (e) { /* login screen handles 401 */ } finally { doctorBusy = false; }
}
async function recheck() {
  const b = $("#recheck"), old = [...b.childNodes]; b.disabled = true; b.replaceChildren(h("span", { class: "spin" }), "Checking…");
  try { formDirty = false; await loadDoctor(true); toast(doctor && doctor.ready ? "Everything looks good" : "Some things need attention", doctor && doctor.ready ? "" : "bad"); }
  finally { b.disabled = false; b.replaceChildren(...old); }
}
async function saveConfig(reset) {
  const b = $("#cfgSave"), old = [...b.childNodes]; b.disabled = true; b.replaceChildren(h("span", { class: "spin" }), "Checking…"); $("#cfgErr").textContent = "";
  const body = reset ? { nc_container: "", nc_url: "", db_container: "" } : { nc_container: $("#cfgNC").value, nc_url: $("#cfgURL").value, db_container: $("#cfgDB").value };
  try {
    const r = await api("/api/config", body);
    if (r.error) { $("#cfgErr").textContent = r.error; return; }
    doctor = r; formDirty = false; renderSetup(); poll();
    toast(r.ready ? "Saved. Everything looks good" : "Saved, but some checks still fail", r.ready ? "" : "bad");
  } finally { b.disabled = false; b.replaceChildren(...old); }
}

function renderPwCard() {
  const env = authMode === "env";
  $("#pwFields").classList.toggle("hidden", env);
  $("#pwNote").textContent = env ? "Your password is set by the PANEL_PASSWORD setting. To change it, edit that setting and recreate the container." : "Change the password you use to sign in. Anyone else who is signed in will be signed out.";
}

// ---------- boot ----------
function renderAll() { renderStatus(); renderStats(); renderHealth(); renderLogs(); renderTables(); }
async function poll() {
  if (document.hidden) return;
  try {
    Object.assign(S, await api("/api/status")); renderAll();
    if (!sharesLoaded || S.url !== lastUrl || tick % 7 === 0) loadShares();
    lastUrl = S.url; if (tick % 20 === 0) loadUpdate(); if (!doctor || tick % 10 === 0) loadDoctor(false); tick++;
  } catch (e) { /* login screen handles 401 */ }
}
function bind() {
  for (let i = 0; i < 24; i++) $("#hour").append(new Option(String(i).padStart(2, "0") + ":00", i));
  $("#filters").replaceChildren(...[["all", "All"], ["folder", "Folders"], ["video", "Videos"], ["image", "Images"], ["audio", "Audio"], ["file", "Files"]].map(([v, l]) => h("button", { class: v === "all" ? "on" : "", "data-f": v, onclick: e => { filter = v; $$("#filters button").forEach(b => b.classList.toggle("on", b === e.currentTarget)); renderTables(); } }, l)));
  $("#q").oninput = e => { query = e.target.value.trim().toLowerCase(); renderTables(); };
  $("#sort").onchange = e => { sortBy = e.target.value; renderTables(); };
  $("#refresh").onclick = async e => { const b = e.currentTarget; b.disabled = true; await loadShares(); b.disabled = false; toast("Share list refreshed"); };
  $$("#logFilter button").forEach(b => b.onclick = () => { logErrOnly = b.dataset.f === "err"; $$("#logFilter button").forEach(x => x.classList.toggle("on", x === b)); renderLogs(); });
  $("#logDl").onclick = () => { const a = h("a", { href: URL.createObjectURL(new Blob([S.logs.join("\n") + "\n"], { type: "text/plain" })), download: "sharetunnel.log" }); document.body.append(a); a.click(); a.remove(); };
  $("#recheck").onclick = recheck; $("#cfgSave").onclick = () => saveConfig(false); $("#cfgReset").onclick = () => saveConfig(true);
  ["#cfgNC", "#cfgURL", "#cfgDB"].forEach(id => $(id).oninput = () => { formDirty = true; });
  $("#cmdCopy").onclick = e => copy($("#cmdPs").textContent, e.currentTarget);
  $("#checkBtn").onclick = checkNow; $("#applyBtn").onclick = applyNow;
  $$("#modes input").forEach(r => r.onchange = saveSettings); $("#hour").onchange = saveSettings;
  $("#toggle").onclick = async e => { const b = e.currentTarget; b.disabled = true; await api(S.status === "stopped" ? "/api/start" : "/api/stop", {}); b.disabled = false; poll(); };
  $("#themeBtn").onclick = () => { const t = document.documentElement.dataset.theme === "dark" ? "light" : "dark"; document.documentElement.dataset.theme = t; try { localStorage.setItem("st-theme", t); } catch (e) {} paintTheme(); };
  $("#menuBtn").onclick = () => $("#app").classList.toggle("nav-open"); $("#scrim").onclick = () => $("#app").classList.remove("nav-open");
  $("#logout").onclick = async () => { await api("/api/logout", {}); showLogin(); };
  $("#pwToggle").onclick = () => { const p = $("#pw"); p.type = p.type === "password" ? "text" : "password"; };
  $("#loginBtn").onclick = async () => {
    const b = $("#loginBtn"); b.disabled = true; const r = await api("/api/login", { password: $("#pw").value }); b.disabled = false;
    if (r.ok) loginDone();
    else if (r.error === "setup_required") showLogin(); // the password was reset while this page was open
    else { $("#loginErr").textContent = r.error || "Sign in failed"; $("#forgot").classList.remove("hidden"); }
  };
  $("#forgotLink").onclick = () => $("#forgot").classList.toggle("hidden");
  $("#cmdResetCopy").onclick = e => copy($("#cmdReset").textContent, e.currentTarget);
  $("#npwToggle").onclick = () => { const t = $("#npw1").type === "password" ? "text" : "password"; $("#npw1").type = t; $("#npw2").type = t; };
  $("#setupBtn").onclick = async () => {
    const a = $("#npw1").value, err = $("#setupErr");
    if (a.length < 8) { err.textContent = "Use at least 8 characters."; return; }
    if (a !== $("#npw2").value) { err.textContent = "The two passwords don't match."; return; }
    const b = $("#setupBtn"); b.disabled = true; const r = await api("/api/setup", { password: a }); b.disabled = false;
    if (r.ok) { authMode = "file"; loginDone(); toast("Password created. Welcome!"); } else err.textContent = r.error || "Could not create the password";
  };
  $("#npw2").addEventListener("keydown", e => { if (e.key === "Enter") $("#setupBtn").click(); });
  $("#pwSave").onclick = async () => {
    const n1 = $("#pwNew").value, err = $("#pwErr"); err.textContent = "";
    if (n1.length < 8) { err.textContent = "Use at least 8 characters."; return; }
    if (n1 !== $("#pwNew2").value) { err.textContent = "The new passwords don't match."; return; }
    const b = $("#pwSave"); b.disabled = true; const r = await api("/api/password", { current: $("#pwCur").value, new: n1 }); b.disabled = false;
    if (r.ok) { ["#pwCur", "#pwNew", "#pwNew2"].forEach(id => $(id).value = ""); toast("Password changed"); } else err.textContent = r.error || "Could not change the password";
  };
  $("#pw").addEventListener("keydown", e => { if (e.key === "Enter") $("#loginBtn").click(); });
  window.addEventListener("hashchange", route); document.addEventListener("visibilitychange", () => { if (!document.hidden) poll(); });
  setInterval(poll, 3000);
}
(async () => {
  paintTheme(); bind(); $("#sharesBox").replaceChildren(h("div", { class: "skel" }, ), h("div", { class: "skel" }), h("div", { class: "skel" }));
  const sess = await api("/api/session");
  if (sess.authenticated) { authMode = sess.mode; showApp(); poll(); } else showLogin();
})();
