const $ = s => document.querySelector(s);
const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };

let state = { url: "", status: "", logs: [] }, upd = {}, lastUrl = null, lastLogs = "", tick = 0, sharesLoaded = false;

// ---------- helpers ----------
async function api(path, body) {
  const r = await fetch(path, { method: body === undefined ? "GET" : "POST",
    headers: { "Content-Type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body) });
  if (r.status === 401 && path !== "/api/login") { showLogin(); throw new Error("login"); }
  return r.json();
}
function toast(msg, kind = "ok") {
  const t = el("div", "toast " + kind, msg); $("#toasts").append(t);
  setTimeout(() => { t.classList.add("out"); setTimeout(() => t.remove(), 300); }, 2600);
}
function copy(text, btn, label) {
  const done = () => {
    toast("Copied ✓"); if (!btn) return;
    const o = btn.textContent; btn.textContent = "Copied ✓"; btn.classList.add("done");
    setTimeout(() => { btn.textContent = o; btn.classList.remove("done"); }, 1500);
  };
  if (navigator.clipboard && window.isSecureContext) { navigator.clipboard.writeText(text).then(done); return; }
  const t = el("textarea"); t.value = text; t.style.cssText = "position:fixed;opacity:0";   // this panel runs on plain http
  document.body.append(t); t.select();
  try { document.execCommand("copy"); done(); } catch (e) { prompt("Copy this link:", text); }
  t.remove();
}
function ago(ts) {
  if (!ts) return "never";
  const s = Math.floor(Date.now() / 1000) - ts;
  if (s < 60) return "just now"; if (s < 3600) return Math.floor(s / 60) + " min ago";
  if (s < 86400) return Math.floor(s / 3600) + " h ago"; return Math.floor(s / 86400) + " d ago";
}
const showLogin = () => { $("#login").classList.remove("hidden"); $("#main").classList.add("hidden"); $("#logout").classList.add("hidden"); };
const showMain = () => { $("#login").classList.add("hidden"); $("#main").classList.remove("hidden"); $("#logout").classList.remove("hidden"); };

// ---------- tunnel status ----------
function renderStatus(s) {
  state = s;
  $("#ver").textContent = s.version;
  $("#pill").className = "pill " + s.status;
  $("#statusText").textContent = { running: "Online", starting: "Starting…", stopped: "Stopped", error: "Error" }[s.status] || s.status;
  const u = $("#url"); u.textContent = s.url || "none";
  if (s.url && s.url !== lastUrl) { u.classList.remove("flash"); void u.offsetWidth; u.classList.add("flash"); }
  $("#copyUrl").disabled = !s.url;
  const on = s.status !== "stopped", b = $("#toggle");
  b.textContent = on ? "Stop" : "Start"; b.className = "btn" + (on ? " bad" : "");
  const joined = s.logs.join("\n");
  if (joined !== lastLogs) {
    lastLogs = joined;
    const pre = $("#logs"), bottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 20;
    pre.textContent = "";
    for (const l of s.logs) pre.append(el("div", /ERR|ERROR|failed|ROLLING/.test(l) ? "e" : /is up|trusts|UPDATE OK/.test(l) ? "g" : "", l));
    if (bottom) pre.scrollTop = pre.scrollHeight;
  }
}

// ---------- shares ----------
function skeleton() { const b = $("#shares"); b.textContent = ""; b.append(el("div", "skel"), el("div", "skel")); }
async function loadShares() {
  const d = await api("/api/shares");
  $("#shareErr").textContent = d.error ? "Could not read shares: " + d.error : "";
  const box = $("#shares"); box.textContent = "";
  if (!d.shares.length) { box.append(el("div", "dim", "No public share links yet. Create one in Nextcloud and it shows up here.")); return; }
  d.shares.forEach((s, i) => {
    const row = el("div", "share"); row.style.setProperty("--i", i);
    const mid = el("div", "grow");
    let meta = s.kind + " · " + s.size_text + " · " + s.owner;
    if (s.has_pw) meta += " · 🔒 password";
    if (s.expiration) meta += " · ⏳ expires " + s.expiration;
    mid.append(el("div", "name", s.file_target.replace(/^\//, "")), el("div", "dim", meta));
    const btn = el("button", "btn sm", "Copy link"); btn.disabled = !state.url;
    btn.onclick = () => copy(state.url + "/s/" + s.token, btn);
    row.append(el("div", "icon", s.icon), mid, btn); box.append(row);
  });
}

// ---------- updates ----------
function renderUpdate() {
  $("#curVer").textContent = upd.current || "–";
  $("#latestVer").textContent = upd.latest || "–";
  $("#checked").textContent = ago(upd.checked_at);
  const msg = $("#updMsg");
  if (upd.error) { msg.className = "msg bad"; msg.textContent = upd.error; }
  else if (upd.dev) { msg.className = "msg"; msg.textContent = "Development build: automatic updates only apply to official releases."; }
  else if (upd.checked_at && !upd.available) { msg.className = "msg ok"; msg.textContent = "You're up to date ✓"; }
  else { msg.className = "msg"; msg.textContent = ""; }
  $("#banner").classList.toggle("hidden", !upd.available);
  if (upd.available) {
    $("#bannerTitle").textContent = "Version " + upd.latest + " is available";
    $("#bannerSub").textContent = "You have " + upd.current + ". Your tunnel is back about 30 seconds after updating.";
    $("#notes").textContent = upd.notes || "No release notes.";
  }
  $("#mode").value = upd.mode || "notify";
  $("#hourBox").classList.toggle("hidden", upd.mode !== "auto");
  $("#hour").value = upd.hour;
}
async function loadUpdate() { upd = await api("/api/update"); renderUpdate(); }

async function checkNow() {
  const b = $("#checkBtn"); b.disabled = true; b.querySelector(".spin").classList.remove("hidden"); b.querySelector(".lbl").textContent = "Checking…";
  try {
    upd = await api("/api/update/check", {}); renderUpdate();
    toast(upd.error ? "Check failed" : upd.available ? "Update available: " + upd.latest : "You're up to date ✓", upd.error ? "bad" : "ok");
  } finally { b.disabled = false; b.querySelector(".spin").classList.add("hidden"); b.querySelector(".lbl").textContent = "Check for updates"; }
}

async function applyNow() {
  if (!confirm("Update to " + upd.latest + "? The tunnel restarts and the page reloads.")) return;
  const r = await api("/api/update/apply", {});
  if (r.error) { toast(r.error, "bad"); return; }
  $("#overlay").classList.remove("hidden");
  const old = upd.current, start = Date.now();
  const timer = setInterval(async () => {
    try {
      const h = await (await fetch("/healthz", { cache: "no-store" })).json();
      if (h.version && h.version !== old) { clearInterval(timer); $("#ovTitle").textContent = "Updated to " + h.version + " ✓"; setTimeout(() => location.reload(), 900); }
    } catch (e) { /* container is swapping, keep waiting */ }
    if (Date.now() - start > 150000) { clearInterval(timer); $("#ovTitle").textContent = "Taking longer than expected"; $("#ovSub").textContent = "Your old version may have been restored. Reload the page, or check the log."; }
  }, 2000);
}

async function saveSettings() {
  upd = await api("/api/update/settings", { mode: $("#mode").value, hour: +$("#hour").value });
  renderUpdate(); toast("Saved");
}

// ---------- boot ----------
async function poll() {
  try {
    renderStatus(await api("/api/status"));
    if (!sharesLoaded || state.url !== lastUrl || tick % 7 === 0) { sharesLoaded = true; loadShares(); }
    lastUrl = state.url;
    if (tick % 20 === 0) loadUpdate();
    tick++;
  } catch (e) {}
}

for (let h = 0; h < 24; h++) $("#hour").append(new Option(String(h).padStart(2, "0") + ":00", h));
$("#loginBtn").onclick = async () => {
  const r = await api("/api/login", { password: $("#pw").value });
  if (r.ok) { $("#loginErr").textContent = ""; $("#pw").value = ""; showMain(); tick = 0; poll(); } else $("#loginErr").textContent = r.error || "Login failed";
};
$("#pw").addEventListener("keydown", e => { if (e.key === "Enter") $("#loginBtn").click(); });
$("#logout").onclick = async () => { await api("/api/logout", {}); showLogin(); };
$("#copyUrl").onclick = e => copy(state.url, e.currentTarget);
$("#refresh").onclick = () => { skeleton(); loadShares(); };
$("#checkBtn").onclick = checkNow;
$("#applyBtn").onclick = applyNow;
$("#mode").onchange = saveSettings; $("#hour").onchange = saveSettings;
$("#toggle").onclick = async e => {
  const b = e.currentTarget; b.disabled = true;
  await api(state.status === "stopped" ? "/api/start" : "/api/stop", {});
  b.disabled = false; poll();
};

(async () => {
  skeleton();
  try { renderStatus(await api("/api/status")); showMain(); poll(); } catch (e) {}
  setInterval(() => { if (!$("#main").classList.contains("hidden")) poll(); }, 3000);
})();
