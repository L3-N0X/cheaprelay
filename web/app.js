"use strict";

// ---------- Helpers ----------
const $ = (id) => document.getElementById(id);
const store = {
  get(k, d) { try { const v = localStorage.getItem(k); return v === null ? d : v; } catch (_) { return d; } },
  set(k, v) { try { v === null ? localStorage.removeItem(k) : localStorage.setItem(k, v); } catch (_) {} },
};

const ICONS = {
  play: '<path d="M6 4.5v15L19 12z" fill="currentColor" stroke="none"/>',
  stop: '<rect x="6" y="6" width="12" height="12" rx="2" fill="currentColor" stroke="none"/>',
  coffee: '<path d="M10 2v2M14 2v2M6 2v2M16 8a1 1 0 0 1 1 1v8a4 4 0 0 1-4 4H7a4 4 0 0 1-4-4V9a1 1 0 0 1 1-1h14a4 4 0 1 1 0 8h-1"/>',
  refresh: '<path d="M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/>',
  sliders: '<path d="M21 4h-7M10 4H3M21 12h-9M8 12H3M21 20h-5M12 20H3M14 2v4M8 10v4M16 18v4"/>',
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41"/>',
  moon: '<path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z"/>',
  copy: '<rect width="14" height="14" x="8" y="8" rx="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/>',
  check: '<path d="M20 6 9 17l-5-5"/>',
  x: '<path d="M18 6 6 18M6 6l12 12"/>',
  lock: '<rect width="18" height="11" x="3" y="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>',
  alert: '<path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z"/><path d="M12 9v4M12 17h.01"/>',
  phone: '<rect width="14" height="20" x="5" y="2" rx="2"/><path d="M12 18h.01"/>',
  videoOff: '<path d="M10.66 6H14a2 2 0 0 1 2 2v2.5l5.25-3.06a.5.5 0 0 1 .75.43v8.2"/><path d="M16 16a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h2"/><path d="m2 2 20 20"/>',
  eyeOff: '<path d="M9.88 9.88a3 3 0 1 0 4.24 4.24"/><path d="M10.73 5.08A10.43 10.43 0 0 1 12 5c7 0 10 7 10 7a13.16 13.16 0 0 1-1.67 2.68"/><path d="M6.61 6.61A13.53 13.53 0 0 0 2 12s3 7 10 7a9.74 9.74 0 0 0 5.39-1.61"/><path d="m2 2 20 20"/>',
  eye: '<path d="M2 12s3-7 10-7 10 7 10 7-3 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/>',
  expand: '<path d="M8 3H5a2 2 0 0 0-2 2v3M21 8V5a2 2 0 0 0-2-2h-3M3 16v3a2 2 0 0 0 2 2h3M16 21h3a2 2 0 0 0 2-2v-3"/>',
  reset: '<path d="M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8"/><path d="M3 3v5h5"/>',
  logout: '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9"/>',
  key: '<circle cx="7.5" cy="15.5" r="5.5"/><path d="m21 2-9.6 9.6M15.5 7.5l3 3L22 7l-3-3"/>',
};
const icon = (name) => `<svg class="i" viewBox="0 0 24 24" aria-hidden="true">${ICONS[name] || ""}</svg>`;
const paintIcons = (root = document) => root.querySelectorAll("[data-icon]").forEach((el) => {
  if (!el.firstElementChild) el.innerHTML = icon(el.dataset.icon);
});

function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k === "html") el.innerHTML = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) if (c != null && c !== false) el.append(c.nodeType ? c : String(c));
  return el;
}

function dur(ms) {
  if (ms == null || !isFinite(ms)) return "—";
  const s = Math.max(0, Math.floor(ms / 1000));
  const hh = Math.floor(s / 3600), mm = Math.floor((s % 3600) / 60), ss = s % 60;
  const p = (n) => String(n).padStart(2, "0");
  return hh ? `${hh}:${p(mm)}:${p(ss)}` : `${mm}:${p(ss)}`;
}
function ago(iso) {
  if (!iso) return null;
  return Math.max(0, Date.now() - new Date(iso).getTime());
}
function human(ms) {
  const s = Math.round(ms / 1000);
  if (s < 60) return s + "s";
  if (s < 3600) return Math.floor(s / 60) + "m " + (s % 60) + "s";
  if (s < 86400) return Math.floor(s / 3600) + "h " + Math.floor((s % 3600) / 60) + "m";
  return Math.floor(s / 86400) + "d " + Math.floor((s % 86400) / 3600) + "h";
}
const setText = (id, v) => { const el = $(id); if (el.textContent !== String(v)) el.textContent = v; };

// ---------- API ----------
let token = store.get("relayToken", "");
// "/#token=..." logs in once (e.g. from a bookmark) without the token ever reaching the server logs.
{
  const m = location.hash.match(/token=([^&]+)/);
  if (m) {
    token = decodeURIComponent(m[1]);
    store.set("relayToken", token);
    history.replaceState(null, "", location.pathname + location.search);
  }
}

class HTTPError extends Error {
  constructor(status, msg) { super(msg); this.status = status; }
}

async function api(method, path, body) {
  const headers = {};
  if (token) headers.Authorization = "Bearer " + token;
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), cache: "no-store" });
  if (res.status === 401) { showGate(token ? "That token didn't work." : ""); throw new HTTPError(401, "unauthorized"); }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new HTTPError(res.status, data.error || res.statusText);
  return data;
}

// ---------- Toasts ----------
function toast(msg, { type = "ok", action, timeout = 4000 } = {}) {
  const el = h("div", { class: "toast " + type, role: "status" },
    h("span", { class: "t-icon", html: icon(type === "error" ? "alert" : "check") }),
    h("span", {}, msg));
  if (action) el.append(h("button", { class: "btn btn-ghost", onclick: () => { action.fn(); close(); } }, action.label));
  const close = () => { el.classList.add("out"); setTimeout(() => el.remove(), 200); };
  $("toasts").append(el);
  setTimeout(close, action ? timeout * 2 : timeout);
}

// ---------- State ----------
let status = null;
const HISTORY = 60;
const hist = { bitrate: [], rtt: [], loss: [], data: [] };
let prevLost = null;
let previewOn = store.get("relayPreview", "1") === "1";
let shownSeq = 0;
let frameLoading = false;
let logSeq = 0;
let logFilter = "all";
const logEntries = [];

// ---------- Theme ----------
function effectiveTheme() {
  const t = document.documentElement.dataset.theme;
  if (t) return t;
  return matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
}
function setTheme(t) {
  if (t) document.documentElement.dataset.theme = t;
  else delete document.documentElement.dataset.theme;
  store.set("relayTheme", t || null);
  $("theme").innerHTML = icon(effectiveTheme() === "dark" ? "sun" : "moon");
}
$("theme").addEventListener("click", () => setTheme(effectiveTheme() === "dark" ? "light" : "dark"));
matchMedia("(prefers-color-scheme: light)").addEventListener("change", () => setTheme(document.documentElement.dataset.theme));

// ---------- Render ----------
function broadcastState(o) {
  if (!o.running && o.mode === "stopped") return "off";
  if (o.running && !o.ffmpeg_running) return "reconnecting";
  if (o.mode === "live") return "live";
  if (o.mode === "fallback") return o.forced_fallback ? "forced" : "fallback";
  return "off";
}

function render(st) {
  status = st;
  const o = st.output, i = st.input, r = st.relay;
  const state = broadcastState(o);

  // Hero
  const labels = { off: "Off air", reconnecting: "Reconnecting", live: "Live", fallback: "Fallback", forced: "BRB" };
  const titles = { off: "Not streaming", reconnecting: "Reconnecting to Twitch", live: "Live on Twitch", fallback: "Showing fallback", forced: "Be right back" };
  $("hero").dataset.state = state;
  setText("hero-label", labels[state]);
  setText("hero-title", titles[state]);
  setText("hero-desc", describe(state, o, i, r));
  setText("stat-onair", o.started_at ? dur(ago(o.started_at)) : "—");
  setText("stat-mode", o.running ? dur(ago(o.mode_since)) : "—");
  setText("stat-switches", o.switches);
  setText("stat-restarts", o.restarts);
  document.title = (state === "live" ? "● Live" : labels[state]) + " · Relay";

  const errAge = ago(o.last_error_at);
  $("error").hidden = !o.last_error;
  if (o.last_error) {
    $("error").classList.toggle("stale", errAge > 5 * 60 * 1000);
    setText("error-title", "Last error · " + human(errAge) + " ago");
    setText("error-msg", o.last_error);
  }

  // Controls
  const needsKey = !r.stream_key_set;
  for (const id of ["btn-start", "dock-start"]) {
    const b = $(id);
    b.hidden = o.running;
    b.lastElementChild.textContent = needsKey ? "Set stream key" : (id === "dock-start" ? "Go live" : "Go live");
    b.firstElementChild.innerHTML = icon(needsKey ? "key" : "play");
  }
  $("btn-stop").hidden = $("dock-stop").hidden = !o.running;
  $("seg-auto").setAttribute("aria-pressed", String(!o.forced_fallback));
  $("seg-brb").setAttribute("aria-pressed", String(o.forced_fallback));
  $("dock-brb").setAttribute("aria-pressed", String(o.forced_fallback));
  $("btn-reconnect").disabled = $("dock-reconnect").disabled = !o.running;

  renderMetrics(i);
  renderPhone(i, r);
  renderPreview(st);
  if (r.log_seq > logSeq) fetchLogs();
}

function describe(state, o, i, r) {
  switch (state) {
    case "off":
      if (!r.stream_key_set) return "Add your Twitch stream key in the settings to get started.";
      return i.active ? "The phone is sending. Go live and it's on air right away." : "Start the stream to go live on Twitch. The fallback is shown until the phone sends.";
    case "reconnecting": return "The connection to Twitch dropped. Retrying automatically…";
    case "live": return "The phone's signal is on air.";
    case "forced": return "Manual BRB: the fallback is on air even though the phone may be fine.";
    case "fallback": {
      if (i.video_codec === "hevc") return "The phone sends H.265. Twitch needs H.264, change the codec in the app.";
      if (i.active && i.active_since) {
        const left = r.live_stable_ms - ago(i.active_since);
        return left > 0 ? `Signal is back. Going live in ${Math.ceil(left / 1000)}s…` : "Signal is back. Going live…";
      }
      let s = i.connected ? "The phone is connected but sends no data." : "Phone signal lost. Viewers see the fallback.";
      if (r.fallback_max_s > 0) s += ` Stream ends in ${dur(r.fallback_max_s * 1000 - ago(o.mode_since))} if it stays away.`;
      return s;
    }
  }
  return "";
}

function push(arr, v) { arr.push(v); if (arr.length > HISTORY) arr.shift(); }

function renderMetrics(i) {
  const lost = i.packets_lost + i.packets_dropped;
  const lossRate = prevLost == null || !i.connected ? 0 : Math.max(0, lost - prevLost);
  prevLost = i.connected ? lost : null;
  push(hist.bitrate, i.connected ? i.bitrate_kbps : 0);
  push(hist.rtt, i.connected ? i.rtt_ms : null);
  push(hist.loss, i.connected ? lossRate : null);
  push(hist.data, i.connected && i.last_data_ms_ago != null ? Math.min(i.last_data_ms_ago, 5000) : null);

  const mb = i.bitrate_kbps / 1000;
  setText("m-bitrate", i.connected ? (mb >= 10 ? mb.toFixed(1) : mb.toFixed(2)) : "—");
  const peak = Math.max(...hist.bitrate);
  setText("m-bitrate-sub", peak ? "peak " + (peak / 1000).toFixed(1) : "");
  setText("m-rtt", i.connected ? Math.round(i.rtt_ms) : "—");
  setText("m-rtt-sub", i.connected && status ? "lat " + (status.relay.srt_latency_ms / 1000) + "s" : "");
  setText("m-loss", i.connected ? lossRate : "—");
  setText("m-loss-sub", i.connected ? `${i.packets_lost} lost · ${i.packets_dropped} drop` : "");
  setText("m-data", i.connected && i.last_data_ms_ago != null ? i.last_data_ms_ago : "—");
  setText("m-data-sub", i.keyframe_cache_bytes ? "gop " + Math.round(i.keyframe_cache_bytes / 1024) + " KB" : "");

  const tone = (el, t) => { el.closest(".metric").dataset.tone = t; };
  tone($("m-bitrate"), !i.connected ? "" : i.bitrate_kbps > 7000 ? "warn" : i.active ? "good" : "bad");
  tone($("m-rtt"), !i.connected ? "" : i.rtt_ms * 4 > status.relay.srt_latency_ms ? "warn" : "good");
  tone($("m-loss"), !i.connected ? "" : i.packets_dropped > 0 && lossRate > 0 ? "bad" : lossRate > 0 ? "warn" : "good");
  tone($("m-data"), !i.connected ? "" : i.active ? "good" : "bad");

  spark("s-bitrate", hist.bitrate);
  spark("s-rtt", hist.rtt);
  spark("s-loss", hist.loss, 5);
  spark("s-data", hist.data, 200);
}

function spark(id, values, minMax = 1) {
  const svg = $(id);
  const W = 100, H = 40;
  svg.setAttribute("viewBox", `0 0 ${W} ${H}`);
  const nums = values.filter((v) => v != null);
  if (nums.length < 2) { svg.innerHTML = ""; return; }
  const max = Math.max(minMax, ...nums) * 1.15;
  const step = W / (HISTORY - 1);
  const off = (HISTORY - values.length) * step;
  let d = "", first = null, last = null;
  values.forEach((v, k) => {
    if (v == null) return;
    const x = off + k * step, y = H - (v / max) * (H - 4) - 1;
    d += (d ? "L" : "M") + x.toFixed(2) + " " + y.toFixed(2);
    if (first == null) first = x;
    last = x;
  });
  svg.innerHTML = `<path class="area" d="${d}L${last} ${H}L${first} ${H}Z"/><path class="line" d="${d}"/>`;
}

function renderPhone(i, r) {
  const pill = $("phone-pill");
  const st = i.connected ? (i.active ? "ok" : "warn") : "off";
  pill.dataset.state = st;
  pill.lastElementChild.textContent = { ok: "Sending", warn: "No data", off: "Not connected" }[st];

  const codec = { h264: "H.264", hevc: "H.265 · needs H.264" }[i.video_codec] || "—";
  const rows = [
    ["Address", i.remote_addr || "—"],
    ["Connected", i.connected ? human(ago(i.connected_at)) : "—"],
    ["Stream ID", i.stream_id || "—"],
    ["Video", codec, i.video_codec === "hevc"],
  ];
  const kv = $("phone-kv");
  if (kv.children.length !== rows.length * 2) {
    kv.replaceChildren(...rows.flatMap(() => [h("dt"), h("dd")]));
  }
  rows.forEach(([k, v, bad], n) => {
    kv.children[n * 2].textContent = k;
    const dd = kv.children[n * 2 + 1];
    if (dd.textContent !== v) dd.textContent = v;
    dd.title = v;
    dd.classList.toggle("bad", !!bad);
  });

  let url = `srt://${location.hostname}:${r.srt_port || "9000"}`;
  const q = [];
  if (r.srt_stream_id) q.push("streamid=" + encodeURIComponent(r.srt_stream_id));
  if (q.length) url += "?" + q.join("&");
  setText("srt-url", url);
  setText("srt-hint", (r.srt_passphrase
    ? "Encrypted: set the same passphrase in the app."
    : "No passphrase set, anyone who knows this address can send. Set one in the settings.")
    + ` Mode caller, H.264, keyframe interval 1s, latency ≥ ${r.srt_latency_ms} ms.`);
}

// ---------- Preview ----------
function wantPreview() {
  return previewOn && !document.hidden && (!status || status.relay.preview_enabled);
}

function renderPreview(st) {
  const r = st.relay, i = st.input, f = st.preview;
  const stage = $("stage");
  const toggle = $("pv-toggle");
  toggle.checked = previewOn && r.preview_enabled;
  toggle.disabled = !r.preview_enabled;

  // codec / resolution chips
  const codec = { h264: "H.264", hevc: "H.265" }[i.video_codec];
  $("pv-codec").hidden = !codec || !i.connected;
  setText("pv-codec", codec || "");
  $("pv-codec").classList.toggle("warn", i.video_codec === "hevc");
  const showRes = previewOn && r.preview_enabled && f && f.width && i.connected;
  $("pv-res").hidden = !showRes;
  if (showRes) setText("pv-res", `${f.width}×${f.height}${f.fps ? " · " + Math.round(f.fps) + "fps" : ""}`);

  let state, title = "", sub = "", iconName = "videoOff";
  const frameAge = f ? ago(f.at) : null;
  if (!r.preview_enabled) {
    state = "off"; iconName = "eyeOff"; title = "Preview disabled"; sub = "Turn on “Input preview” in the settings.";
  } else if (!previewOn) {
    state = "off"; iconName = "eyeOff"; title = "Preview paused"; sub = "Turn it on to see a snapshot of the phone's stream about every second.";
  } else if (!i.connected) {
    state = f && shownSeq ? "stale" : "nosignal"; title = "No signal";
    sub = state === "stale" ? `Phone disconnected, last frame ${human(frameAge)} ago.` : "Waiting for the phone to connect.";
  } else if (!f || !shownSeq) {
    state = "nosignal"; title = i.active ? "Waiting for a keyframe…" : "Connected, no data"; sub = i.active ? "The first snapshot appears with the next keyframe." : "The phone is connected but doesn't send anything.";
  } else if (!i.active || frameAge > 6000) {
    state = "stale"; title = "Signal interrupted"; sub = "Last frame " + human(frameAge) + " ago.";
  } else {
    state = "ok";
  }
  stage.dataset.state = state;
  setText("stage-title", title);
  setText("stage-sub", sub);
  const ic = $("stage-empty").querySelector(".stage-icon");
  if (ic.dataset.icon !== iconName) { ic.dataset.icon = iconName; ic.innerHTML = icon(iconName); }
  $("pv-tag").lastElementChild.textContent = state === "ok" ? "Input" : state === "stale" ? "Stale" : "Input";
  setText("pv-age", previewOn && r.preview_enabled && f && state === "ok" ? "updated " + (frameAge / 1000).toFixed(1) + "s ago" : "");

  // audio meter
  const showMeter = previewOn && r.preview_enabled && f && i.connected && state !== "off";
  $("meter").hidden = !showMeter || f.audio_peak_db == null && f.audio_mean_db == null && !i.active;
  if (showMeter) {
    const pct = (db) => db == null ? 0 : Math.max(0, Math.min(100, (db + 60) / 60 * 100));
    $("meter-fill").style.width = pct(f.audio_mean_db) + "%";
    $("meter-peak").style.left = `calc(${pct(f.audio_peak_db)}% - 2px)`;
    $("meter-fill").style.setProperty("--meter-w", $("meter-fill").parentElement.clientWidth + "px");
    setText("meter-val", f.audio_peak_db == null ? "no audio" : f.audio_peak_db <= -80 ? "silent" : f.audio_peak_db.toFixed(0) + " dB");
  }

  if (wantPreview() && f && f.seq !== shownSeq && !frameLoading) loadFrame(f.seq);
}

async function loadFrame(seq) {
  frameLoading = true;
  try {
    const res = await fetch("/api/preview.jpg?seq=" + seq, { headers: token ? { Authorization: "Bearer " + token } : {} });
    if (res.status !== 200) return;
    const url = URL.createObjectURL(await res.blob());
    const img = new Image();
    img.src = url;
    await img.decode().catch(() => {});
    const el = $("pv-img");
    const old = el.src;
    el.src = url;
    if (old.startsWith("blob:")) URL.revokeObjectURL(old);
    shownSeq = seq;
    if (status) renderPreview(status);
  } catch (_) {
  } finally {
    frameLoading = false;
  }
}

$("pv-toggle").addEventListener("change", (e) => {
  previewOn = e.target.checked;
  store.set("relayPreview", previewOn ? "1" : "0");
  if (status) renderPreview(status);
  if (previewOn) poll();
});
$("pv-full").addEventListener("click", () => {
  const s = $("stage");
  if (document.fullscreenElement) document.exitFullscreen();
  else if (s.requestFullscreen) s.requestFullscreen().catch(() => {});
  else if ($("pv-img").webkitEnterFullscreen) $("pv-img").webkitEnterFullscreen();
});

// ---------- Activity ----------
async function fetchLogs() {
  if (fetchLogs.busy) return;
  fetchLogs.busy = true;
  try {
    const entries = await api("GET", "/api/logs?after=" + logSeq);
    const first = logSeq === 0;
    for (const e of entries) { logEntries.push(e); logSeq = Math.max(logSeq, e.seq); }
    if (logEntries.length > 300) logEntries.splice(0, logEntries.length - 300);
    renderLogs(first ? 0 : entries.length);
  } catch (_) {
  } finally {
    fetchLogs.busy = false;
  }
}

function renderLogs(fresh = 0) {
  const list = $("log");
  const show = logEntries.filter((e) => logFilter === "all" || e.level === "warn" || e.level === "error");
  const items = show.slice(-150).reverse().map((e, n) => {
    const t = new Date(e.time);
    const li = h("li", { class: (e.level === "error" ? "error" : "") + (n < fresh ? " new" : "") },
      h("time", { class: "mono", datetime: e.time, title: t.toLocaleString() }, t.toLocaleTimeString([], { hour12: false })),
      h("span", { class: "lvl " + e.level, title: e.level }),
      h("span", { class: "msg" }, e.component ? h("span", { class: "cmp" }, e.component) : null, e.msg));
    if (e.attrs) {
      li.append(h("span", { class: "attrs", title: e.attrs }, e.attrs));
      li.addEventListener("click", () => li.classList.toggle("open"));
    }
    return li;
  });
  if (!items.length) items.push(h("li", { class: "log-empty" }, logFilter === "all" ? "Nothing happened yet." : "No warnings or errors. 🎉"));
  list.replaceChildren(...items);
}

for (const [id, f] of [["log-all", "all"], ["log-warn", "warn"]]) {
  $(id).addEventListener("click", () => {
    logFilter = f;
    $("log-all").setAttribute("aria-pressed", String(f === "all"));
    $("log-warn").setAttribute("aria-pressed", String(f === "warn"));
    renderLogs();
  });
}

// ---------- Actions ----------
async function act(path, btn, okMsg) {
  btn && btn.classList.add("busy");
  try {
    render(await api("POST", path));
    if (okMsg) toast(okMsg);
  } catch (e) {
    if (e.status === 409) toast(e.message, { type: "error", action: { label: "Settings", fn: openSettings } });
    else if (e.status !== 401) toast(e.message, { type: "error" });
  } finally {
    btn && btn.classList.remove("busy");
  }
}

function start(btn) {
  if (status && !status.relay.stream_key_set) { openSettings("TWITCH_STREAM_KEY"); return; }
  act("/api/start", btn, "Stream started");
}
const toggleBRB = (btn) => act(status && status.output.forced_fallback ? "/api/fallback/release" : "/api/fallback/force", btn);
$("btn-start").addEventListener("click", (e) => start(e.currentTarget));
$("dock-start").addEventListener("click", (e) => start(e.currentTarget));
$("seg-auto").addEventListener("click", (e) => status && status.output.forced_fallback && toggleBRB(e.currentTarget));
$("seg-brb").addEventListener("click", (e) => status && !status.output.forced_fallback && toggleBRB(e.currentTarget));
$("dock-brb").addEventListener("click", (e) => toggleBRB(e.currentTarget));
$("btn-reconnect").addEventListener("click", (e) => act("/api/reconnect", e.currentTarget, "Reconnected to Twitch"));
$("dock-reconnect").addEventListener("click", (e) => act("/api/reconnect", e.currentTarget, "Reconnected to Twitch"));

// Ending the stream ends the Twitch broadcast, so it needs a press-and-hold.
function holdButton(btn, onDone, ms = 900) {
  const label = btn.querySelector(".hold-label");
  const idle = label.textContent;
  let timer = null, done = false;
  btn.style.setProperty("--hold-ms", ms + "ms");
  const begin = (e) => {
    if (timer || (e.button !== undefined && e.button !== 0)) return;
    btn.classList.add("holding");
    label.textContent = "Keep holding…";
    timer = setTimeout(() => { reset(); done = true; onDone(btn); }, ms);
  };
  const reset = () => {
    clearTimeout(timer); timer = null;
    btn.classList.remove("holding");
    label.textContent = idle;
  };
  btn.addEventListener("pointerdown", begin);
  for (const ev of ["pointerup", "pointerleave", "pointercancel"]) btn.addEventListener(ev, reset);
  btn.addEventListener("contextmenu", (e) => e.preventDefault());
  btn.addEventListener("keydown", (e) => { if ((e.key === " " || e.key === "Enter") && !e.repeat) { e.preventDefault(); begin(e); } });
  btn.addEventListener("keyup", (e) => { if (e.key === " " || e.key === "Enter") reset(); });
  btn.addEventListener("click", (e) => { if (done || e.detail === 0) { done = false; return; } toast("Press and hold to end the stream", { type: "error", timeout: 2000 }); });
}
holdButton($("btn-stop"), (b) => act("/api/stop", b, "Stream ended"));
holdButton($("dock-stop"), (b) => act("/api/stop", b, "Stream ended"));

$("copy-url").innerHTML = icon("copy");
$("copy-url").addEventListener("click", async () => {
  try {
    await navigator.clipboard.writeText($("srt-url").textContent);
    $("copy-url").innerHTML = icon("check");
    setTimeout(() => ($("copy-url").innerHTML = icon("copy")), 1500);
  } catch (_) { toast("Couldn't copy, select the URL manually", { type: "error" }); }
});

// ---------- Polling ----------
let pollTimer = null;
let failures = 0;

async function poll() {
  clearTimeout(pollTimer);
  try {
    const st = await api("GET", "/api/status" + (wantPreview() ? "?preview=1" : ""));
    failures = 0;
    setConn("ok", "Connected");
    render(st);
  } catch (e) {
    if (e.status !== 401) { failures++; setConn("error", failures > 2 ? "Offline" : "Retrying"); }
  }
  pollTimer = setTimeout(poll, document.hidden ? 5000 : 1000);
}

function setConn(state, label) {
  $("conn").dataset.state = state;
  setText("conn-label", label);
}

document.addEventListener("visibilitychange", () => { if (!document.hidden) poll(); });

// ---------- Token gate ----------
function showGate(msg) {
  $("gate").hidden = false;
  setText("gate-error", msg || "");
  setConn("error", "Locked");
  setTimeout(() => $("gate-token").focus(), 50);
}
$("gate-form").addEventListener("submit", (e) => {
  e.preventDefault();
  token = $("gate-token").value.trim();
  store.set("relayToken", token);
  $("gate").hidden = true;
  $("gate-token").value = "";
  logSeq = 0; logEntries.length = 0;
  poll();
});

// ---------- Settings ----------
let settings = null;
let dirty = {};
let focusKey = null;

$("open-settings").innerHTML = icon("sliders");
$("close-settings").innerHTML = icon("x");
$("open-settings").addEventListener("click", () => openSettings());
$("close-settings").addEventListener("click", closeSettings);
$("scrim").addEventListener("click", closeSettings);
document.addEventListener("keydown", (e) => { if (e.key === "Escape" && $("drawer").classList.contains("open")) closeSettings(); });

async function openSettings(key) {
  focusKey = typeof key === "string" ? key : null;
  const d = $("drawer"), s = $("scrim");
  s.hidden = false;
  requestAnimationFrame(() => { s.classList.add("show"); d.classList.add("open"); });
  d.removeAttribute("inert");
  d.setAttribute("aria-hidden", "false");
  document.body.style.overflow = "hidden";
  try {
    settings = await api("GET", "/api/settings");
    dirty = {};
    renderSettings();
  } catch (e) {
    if (e.status !== 401) $("settings-body").replaceChildren(h("p", { class: "notice" }, "Couldn't load settings: " + e.message));
  }
}

function closeSettings() {
  if (Object.keys(dirty).length && !confirm("Discard unsaved changes?")) return;
  dirty = {};
  const d = $("drawer"), s = $("scrim");
  d.classList.remove("open"); s.classList.remove("show");
  d.setAttribute("inert", ""); d.setAttribute("aria-hidden", "true");
  document.body.style.overflow = "";
  setTimeout(() => (s.hidden = true), 250);
  updateFoot();
}

function renderSettings() {
  const body = $("settings-body");
  const groups = new Map();
  for (const s of settings.settings) {
    if (!groups.has(s.group)) groups.set(s.group, []);
    groups.get(s.group).push(s);
  }
  const nodes = [];
  if (settings.save_error) nodes.push(h("p", { class: "notice" }, "Changes are active but couldn't be saved, they are lost on restart: " + settings.save_error));
  else if (!settings.persisted && settings.file === "") nodes.push(h("p", { class: "notice" }, "SETTINGS_FILE is not set, changes are lost on restart."));
  for (const [name, items] of groups) {
    nodes.push(h("section", { class: "group" },
      h("div", { class: "group-title" }, name),
      h("div", { class: "group-box" }, items.map(fieldNode))));
  }
  nodes.push(browserGroup());
  body.replaceChildren(...nodes);
  setText("settings-sub", settings.persisted ? "Saved to " + settings.file + ", overrides .env" : "Changes here override .env");
  paintIcons(body);
  updateFoot();
  if (focusKey) {
    const el = body.querySelector(`[data-key="${focusKey}"] input, [data-key="${focusKey}"] select`);
    if (el) { el.scrollIntoView({ block: "center" }); setTimeout(() => el.focus(), 350); }
    focusKey = null;
  }
}

function fieldNode(s) {
  const wrap = h("div", { class: "field", "data-key": s.key });
  const label = h("div", { class: "field-label" }, s.label, h("span", { class: "field-key" }, s.key));
  const top = h("div", { class: "field-top" }, label);
  wrap.append(top);

  if (!s.editable) {
    const v = s.kind === "secret" ? (s.is_set ? "set" : "not set") : (s.value || "—");
    wrap.append(h("div", { class: "readonly-val", title: v, html: icon("lock") }, v));
    if (s.help) wrap.append(h("p", { class: "field-help" }, s.help));
    wrap.append(h("div", { class: "field-meta" }, h("span", {}, "From .env, needs a restart to change")));
    return wrap;
  }

  const err = h("p", { class: "field-error" });
  const onChange = (v) => {
    const original = s.kind === "secret" ? null : s.value;
    if (s.kind === "secret" ? v === "" : v === original) delete dirty[s.key];
    else dirty[s.key] = v;
    err.textContent = "";
    wrap.querySelector(".input")?.classList.remove("invalid");
    markDirty(wrap, s.key);
    updateFoot();
  };

  let control;
  if (s.kind === "bool") {
    const input = h("input", { type: "checkbox" });
    input.checked = s.value === "true";
    input.addEventListener("change", () => onChange(String(input.checked)));
    control = h("label", { class: "switch" }, input, h("span", { class: "switch-track" }, h("span", { class: "switch-thumb" })));
    top.append(control);
  } else if (s.kind === "select") {
    control = h("select", { class: "select mono" }, s.options.map((o) => h("option", { value: o, selected: o === s.value }, o)));
    control.addEventListener("change", () => onChange(control.value));
    wrap.append(control);
  } else if (s.kind === "secret") {
    const input = h("input", {
      class: "input mono", type: "password", autocomplete: "new-password", spellcheck: "false",
      placeholder: s.is_set ? "•••••••• (set, type to replace)" : "Not set",
    });
    input.addEventListener("input", () => onChange(input.value));
    const eye = h("button", { class: "icon-btn", type: "button", title: "Show", "aria-label": "Show", html: icon("eye") });
    eye.addEventListener("click", () => {
      const show = input.type === "password";
      input.type = show ? "text" : "password";
      eye.innerHTML = icon(show ? "eyeOff" : "eye");
    });
    control = h("div", { class: "input-wrap" }, input, eye);
    wrap.append(control);
  } else {
    const input = h("input", {
      class: "input mono", type: "text", value: s.value, spellcheck: "false", autocapitalize: "off", autocomplete: "off",
      placeholder: s.kind === "duration" ? "e.g. 1500ms, 3s, 10m" : "",
      inputmode: s.kind === "duration" ? "text" : null,
    });
    input.addEventListener("input", () => onChange(input.value));
    control = input;
    wrap.append(control);
  }

  if (s.help) wrap.append(h("p", { class: "field-help" }, s.help));
  const meta = h("div", { class: "field-meta" }, h("span", {}, "Applies: " + s.applies));
  if (s.overridden) {
    meta.append(h("span", { class: "badge" }, "UI override"));
    const envShown = s.kind === "secret" ? "" : ` (${s.env_value || "empty"})`;
    meta.append(h("button", {
      class: "link-btn", type: "button", title: "Use the value from .env again",
      onclick: () => { dirty[s.key] = null; markDirty(wrap, s.key); updateFoot(); },
    }, "Reset to .env" + envShown));
  }
  if (s.kind === "secret" && s.is_set && s.key === "SRT_PASSPHRASE") {
    meta.append(h("button", { class: "link-btn", type: "button", onclick: () => { dirty[s.key] = ""; markDirty(wrap, s.key); updateFoot(); } }, "Remove passphrase"));
  }
  meta.append(h("span", { class: "badge dirty", hidden: true }, "unsaved"));
  wrap.append(meta, err);
  return wrap;
}

function markDirty(wrap, key) {
  const b = wrap.querySelector(".badge.dirty");
  if (!b) return;
  const has = key in dirty;
  b.hidden = !has;
  b.textContent = dirty[key] === null ? "will reset" : dirty[key] === "" && key === "SRT_PASSPHRASE" ? "will remove" : "unsaved";
}

function browserGroup() {
  const theme = h("select", { class: "select" },
    [["", "System"], ["dark", "Dark"], ["light", "Light"]].map(([v, l]) => h("option", { value: v, selected: (document.documentElement.dataset.theme || "") === v }, l)));
  theme.addEventListener("change", () => setTheme(theme.value || null));
  const pv = h("input", { type: "checkbox" });
  pv.checked = previewOn;
  pv.addEventListener("change", () => { previewOn = pv.checked; store.set("relayPreview", previewOn ? "1" : "0"); if (status) renderPreview(status); });
  const fields = [
    h("div", { class: "field" }, h("div", { class: "field-top" }, h("div", { class: "field-label" }, "Theme")), theme),
    h("div", { class: "field" },
      h("div", { class: "field-top" }, h("div", { class: "field-label" }, "Show preview"),
        h("label", { class: "switch" }, pv, h("span", { class: "switch-track" }, h("span", { class: "switch-thumb" })))),
      h("p", { class: "field-help" }, "Only on this device. Turn off to save mobile data (≈30–60 KB per second).")),
  ];
  if (token) {
    fields.push(h("div", { class: "field" },
      h("div", { class: "field-top" },
        h("div", { class: "field-label" }, "API token", h("span", { class: "field-key" }, "…" + token.slice(-4))),
        h("button", {
          class: "btn btn-ghost", type: "button",
          onclick: () => { store.set("relayToken", null); token = ""; closeSettings(); poll(); },
        }, h("span", { html: icon("logout") }), "Forget")),
      h("p", { class: "field-help" }, "Stored in this browser only.")));
  }
  return h("section", { class: "group" }, h("div", { class: "group-title" }, "This browser"), h("div", { class: "group-box" }, fields));
}

function updateFoot() {
  const n = Object.keys(dirty).length;
  $("settings-foot").hidden = n === 0;
  setText("dirty-count", n === 1 ? "1 unsaved change" : n + " unsaved changes");
}

$("settings-discard").addEventListener("click", () => { dirty = {}; renderSettings(); });
$("settings-save").addEventListener("click", async (e) => {
  const btn = e.currentTarget;
  const changes = { ...dirty };
  btn.classList.add("busy");
  try {
    settings = await api("PATCH", "/api/settings", changes);
    dirty = {};
    renderSettings();
    poll();
    const twitch = ["TWITCH_STREAM_KEY", "TWITCH_RTMP_URL"].some((k) => k in changes);
    if (settings.save_error) toast("Applied, but not saved: " + settings.save_error, { type: "error" });
    else if (twitch && status && status.output.running) {
      toast("Saved. Applies on the next Twitch connection.", { action: { label: "Reconnect now", fn: () => act("/api/reconnect", null, "Reconnected to Twitch") } });
    } else toast("Settings saved");
  } catch (err) {
    if (err.status === 401) return;
    const key = (err.message.match(/^([A-Z_]+):?/) || [])[1];
    const field = key && $("settings-body").querySelector(`[data-key="${key}"]`);
    if (field) {
      field.querySelector(".field-error").textContent = err.message.replace(/^[A-Z_]+:\s*/, "");
      field.querySelector(".input")?.classList.add("invalid");
      field.scrollIntoView({ block: "center", behavior: "smooth" });
    } else toast(err.message, { type: "error" });
  } finally {
    btn.classList.remove("busy");
  }
});

// ---------- Boot ----------
paintIcons();
setTheme(document.documentElement.dataset.theme || null);
$("pv-full").innerHTML = icon("expand");
$("pv-toggle").checked = previewOn;
renderLogs();
poll();
// keep timers ticking smoothly between polls
setInterval(() => { if (status && !document.hidden) {
  const o = status.output;
  setText("stat-onair", o.started_at ? dur(ago(o.started_at)) : "—");
  setText("stat-mode", o.running ? dur(ago(o.mode_since)) : "—");
} }, 250);
