"use strict";
// Meteor Imager UI. Secrets (Wi-Fi password, enrollment key) live only in
// the form fields and the single /api/flash request; they are never stored.

const $ = (id) => document.getElementById(id);
const state = {
  source: "server", // or "file"
  server: null, insecure: false, images: [], imageId: null,
  devices: [], device: null,
  step: 1,
};

async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);
  let data = null;
  try { data = await res.json(); } catch (_) { /* not JSON */ }
  if (!res.ok) {
    const err = new Error((data && data.error) || `HTTP ${res.status}`);
    err.problems = data && data.problems;
    throw err;
  }
  return data;
}

function el(tag, attrs = {}, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") e.className = v;
    else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
    else if (v === true) e.setAttribute(k, "");
    else if (v !== false && v != null) e.setAttribute(k, v);
  }
  for (const c of children) if (c != null) e.append(c);
  return e;
}

function fmtSize(n) {
  if (!n) return "";
  const u = ["B", "kB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1000 && i < u.length - 1) { n /= 1000; i++; }
  return `${n.toFixed(i ? 1 : 0)} ${u[i]}`;
}

function showStep(n) {
  state.step = n;
  for (let i = 1; i <= 4; i++) $(`step-${i}`).hidden = i !== n;
  $("progress").hidden = true;
  document.querySelectorAll(".steps li").forEach((li) => {
    const s = Number(li.dataset.step);
    li.classList.toggle("active", s === n);
    li.classList.toggle("done", s < n);
  });
  if (n === 3) loadDevices();
  if (n === 4) renderSummary();
  window.scrollTo(0, 0);
}

// ---------- Step 1: image ----------
function setSource(src) {
  state.source = src;
  $("tab-server").setAttribute("aria-selected", src === "server");
  $("tab-file").setAttribute("aria-selected", src === "file");
  $("pane-server").hidden = src !== "server";
  $("pane-file").hidden = src !== "file";
  updateNext1();
}

function updateNext1() {
  $("next-1").disabled = state.source === "server" ? !state.imageId : !$("file-path").value.trim();
}

async function loadImages() {
  const list = $("images");
  list.replaceChildren(el("div", { class: "empty" }, "Loading…"));
  state.imageId = null;
  updateNext1();
  try {
    const data = await api("POST", "/api/images", { server: $("server").value, insecure_http: $("insecure").checked });
    state.server = $("server").value.trim();
    state.insecure = $("insecure").checked;
    state.images = data.images;
    if (!$("domain").value || $("domain").dataset.auto === "1") {
      $("domain").value = data.meteor_domain;
      $("domain").dataset.auto = "1";
    }
    renderImages();
  } catch (e) {
    list.replaceChildren(el("div", { class: "error" }, e.message));
  }
}

function renderImages() {
  const list = $("images");
  if (!state.images.length) {
    list.replaceChildren(el("div", { class: "empty" },
      "This server publishes no OS images yet. In MeteorCloud, open Artifacts and tick “Show in Meteor Imager” on an OS image."));
    return;
  }
  list.replaceChildren(...state.images.map((im) => {
    const item = el("label", { class: "item" + (im.id === state.imageId ? " selected" : "") },
      el("input", { type: "radio", name: "image", value: im.id, checked: im.id === state.imageId,
        onchange: () => { state.imageId = im.id; renderImages(); updateNext1(); } }),
      el("div", {},
        el("div", { class: "title" }, `${im.name} `, el("span", { class: "muted" }, im.version)),
        el("div", { class: "meta" }, [im.device_type, fmtSize(im.size_bytes), im.file_name].filter(Boolean).join(" · ")),
        im.description ? el("div", { class: "meta" }, im.description) : null));
    return item;
  }));
}

// ---------- Step 2: settings ----------
function buildProfile() {
  const p = { schema_version: 1, hostname: null, timezone: null };
  if ($("use-enroll").checked) {
    p.enrollment = { meteor_domain: $("domain").value.trim(), meteor_api_key: $("api-key").value.trim() };
  }
  if ($("use-wifi").checked) {
    p.wifi = { ssid: $("ssid").value, psk: $("psk").value, hidden: $("hidden-ssid").checked, country: $("country").value.trim().toUpperCase() };
  }
  const host = $("hostname").value.trim();
  const tz = $("timezone").value.trim();
  if (host) p.hostname = host;
  if (tz) p.timezone = tz;
  return p;
}

function checkSettings() {
  const problems = [];
  const p = buildProfile();
  if (!p.enrollment && !p.wifi && !p.hostname && !p.timezone) {
    return { profile: null, problems }; // write the image unchanged
  }
  if (p.enrollment) {
    if (!p.enrollment.meteor_domain) problems.push("Enter the MeteorCloud domain.");
    if (!/^key_\S{4,}$/.test(p.enrollment.meteor_api_key)) problems.push("Enter an enrollment key (starts with key_).");
  }
  if (p.wifi) {
    if (!p.wifi.ssid) problems.push("Enter the Wi-Fi network name.");
    if (p.wifi.psk.length < 8 || p.wifi.psk.length > 63) problems.push("The Wi-Fi password must be 8 to 63 characters.");
    if (!/^[A-Z]{2}$/.test(p.wifi.country)) problems.push("Enter the two-letter Wi-Fi country code, e.g. DE.");
  }
  return { profile: p, problems };
}

function showProblems(problems) {
  const ul = $("problems");
  ul.hidden = !problems.length;
  ul.replaceChildren(...problems.map((t) => el("li", {}, t)));
}

// ---------- Step 3: devices ----------
async function loadDevices() {
  const list = $("devices");
  list.replaceChildren(el("div", { class: "empty" }, "Looking for cards…"));
  try {
    state.devices = await api("GET", "/api/devices" + ($("show-all").checked ? "?all=1" : ""));
  } catch (e) {
    list.replaceChildren(el("div", { class: "error" }, e.message));
    return;
  }
  if (state.device && !state.devices.some((d) => d.device.path === state.device.path)) state.device = null;
  renderDevices();
}

function renderDevices() {
  const list = $("devices");
  $("next-3").disabled = !state.device;
  if (!state.devices.length) {
    list.replaceChildren(el("div", { class: "empty" }, "No card found. Insert an SD card or USB reader, then press Refresh."));
    return;
  }
  list.replaceChildren(...state.devices.map(({ device: d, size }) => {
    const selected = state.device && state.device.path === d.path;
    const notes = [];
    if (!d.removable) notes.push("not removable");
    if (d.read_only) notes.push("read-only");
    if (d.mountpoints && d.mountpoints.length) notes.push("mounted at " + d.mountpoints.join(", "));
    return el("label", { class: "item" + (selected ? " selected" : "") },
      el("input", { type: "radio", name: "device", value: d.path, checked: selected, disabled: d.read_only,
        onchange: () => { state.device = d; renderDevices(); } }),
      el("div", {},
        el("div", { class: "title" }, `${d.model || "Unknown device"} `, el("span", { class: "muted" }, size)),
        el("div", { class: "meta mono" }, d.path + (d.bus ? ` · ${d.bus}` : "")),
        notes.length ? el("div", { class: "meta" }, notes.join(" · ")) : null));
  }));
}

// ---------- Step 4: review and write ----------
function renderSummary() {
  const { profile } = checkSettings();
  const dl = $("summary");
  const rows = [];
  if (state.source === "server") {
    const im = state.images.find((i) => i.id === state.imageId);
    rows.push(["Image", `${im.name} ${im.version} (${fmtSize(im.size_bytes)})`], ["From", state.server]);
  } else {
    rows.push(["Image", $("file-path").value]);
  }
  if (!profile) rows.push(["Settings", "none (image written unchanged)"]);
  if (profile && profile.enrollment) rows.push(["MeteorCloud", `${profile.enrollment.meteor_domain}, key ••••••••`]);
  if (profile && profile.wifi) rows.push(["Wi-Fi", `${profile.wifi.ssid}${profile.wifi.hidden ? " (hidden)" : ""}, ${profile.wifi.country}, password ••••••••`]);
  if (profile && profile.hostname) rows.push(["Hostname", profile.hostname]);
  if (profile && profile.timezone) rows.push(["Timezone", profile.timezone]);
  dl.replaceChildren(...rows.flatMap(([k, v]) => [el("dt", {}, k), el("dd", {}, v)]));
  const d = state.device;
  $("erase-device").textContent = `${d.path} · ${fmtSize(d.size_bytes)} · ${d.model || "Unknown device"}`;
  $("confirm").checked = false;
  $("write").disabled = true;
}

async function startWrite() {
  const { profile } = checkSettings();
  const req = {
    device: state.device.path,
    confirm_device: state.device.path,
    allow_non_removable: !state.device.removable,
    skip_verify: $("skip-verify").checked,
    profile,
    remember: $("remember").checked,
  };
  if (state.source === "server") {
    Object.assign(req, { server: state.server, insecure_http: state.insecure, image_id: state.imageId });
  } else {
    req.path = $("file-path").value.trim();
  }
  $("write").disabled = true;
  try {
    await api("POST", "/api/flash", req);
  } catch (e) {
    if (e.problems) { showStep(2); showProblems(e.problems); return; }
    alert(e.message);
    $("write").disabled = false;
    return;
  }
  showProgress();
}

const STAGES = [
  ["download", "Download"], ["open", "Prepare card"], ["write", "Write"], ["verify", "Verify"],
  ["configure", "Settings"], ["finish", "Finish"], ["eject", "Eject"],
];

function showProgress() {
  for (let i = 1; i <= 4; i++) $(`step-${i}`).hidden = true;
  $("progress").hidden = false;
  $("result").replaceChildren();
  $("cancel").hidden = false;
  $("again").hidden = true;
  $("progress-title").textContent = "Writing…";
  poll();
}

async function poll() {
  let st;
  try { st = await api("GET", "/api/job"); } catch (_) { setTimeout(poll, 1000); return; }
  $("progress-device").textContent = st.device || "";
  const idx = STAGES.findIndex(([k]) => k === st.stage);
  $("stages").replaceChildren(...STAGES
    .filter(([k]) => k !== "download" || state.source === "server")
    .map(([k, label], i) => {
      const si = STAGES.findIndex(([x]) => x === k);
      const cls = st.stage === "done" || si < idx ? "done" : si === idx ? "current" : "";
      return el("li", { class: cls }, label);
    }));
  const pct = Math.round((st.fraction || 0) * 100);
  $("bar-fill").style.width = `${st.stage === "done" ? 100 : pct}%`;
  const label = (STAGES.find(([k]) => k === st.stage) || [null, ""])[1];
  $("progress-label").textContent = st.running ? `${label} ${pct}%` : "";
  $("messages").replaceChildren(...(st.messages || []).map((m) => el("li", {}, m)));

  if (st.running) { setTimeout(poll, 500); return; }
  $("cancel").hidden = true;
  $("again").hidden = false;
  if (st.error) {
    $("progress-title").textContent = "Writing failed";
    $("result").replaceChildren(el("div", { class: "error" }, st.error));
    return;
  }
  $("progress-title").textContent = "Done";
  const r = st.report || {};
  const lines = [el("p", { class: "success" }, `Card written${r.verified ? " and verified" : ""}. You can remove it now.`)];
  if (r.boot && r.boot.written && r.boot.written.length) lines.push(el("p", { class: "muted" }, "Boot partition: " + r.boot.written.join(", ")));
  for (const w of (r.boot && r.boot.warnings) || []) lines.push(el("p", { class: "banner warn" }, w));
  $("result").replaceChildren(...lines);
}

// ---------- wiring ----------
async function init() {
  let st;
  try { st = await api("GET", "/api/state"); } catch (e) {
    document.body.replaceChildren(el("p", { class: "error" }, e.message));
    return;
  }
  $("version").textContent = st.version;
  if (!st.can_write) { $("banner").hidden = false; $("banner").textContent = st.why_not; }
  const p = st.prefs || {};
  $("server").value = p.server_url || "";
  $("domain").value = p.meteor_domain || "";
  $("ssid").value = p.wifi_ssid || "";
  $("hidden-ssid").checked = !!p.wifi_hidden;
  $("country").value = p.wifi_country || guessCountry();
  $("hostname").value = p.hostname || "";
  $("timezone").value = p.timezone || "";
  if (!$("timezone").value) {
    try { $("timezone").placeholder = Intl.DateTimeFormat().resolvedOptions().timeZone || "Europe/Berlin"; } catch (_) { /* ignore */ }
  }
  if (p.server_url) loadImages();

  $("tab-server").onclick = () => setSource("server");
  $("tab-file").onclick = () => setSource("file");
  $("load-images").onclick = loadImages;
  $("server").addEventListener("keydown", (e) => { if (e.key === "Enter") loadImages(); });
  $("file-path").addEventListener("input", updateNext1);
  $("browse").onclick = async () => {
    try {
      const r = await api("POST", "/api/browse", {});
      if (r.path) { $("file-path").value = r.path; updateNext1(); }
    } catch (e) { alert(e.message); }
  };
  $("next-1").onclick = () => showStep(2);
  $("domain").addEventListener("input", () => { $("domain").dataset.auto = "0"; });

  const toggle = (box, fields) => { const f = () => { $(fields).hidden = !$(box).checked; }; $(box).onchange = f; f(); };
  toggle("use-enroll", "enroll-fields");
  toggle("use-wifi", "wifi-fields");
  document.querySelectorAll(".reveal").forEach((b) => {
    b.onclick = () => {
      const input = $(b.dataset.for);
      input.type = input.type === "password" ? "text" : "password";
      b.textContent = input.type === "password" ? "Show" : "Hide";
    };
  });
  $("next-2").onclick = () => {
    const { problems } = checkSettings();
    showProblems(problems);
    if (!problems.length) showStep(3);
  };
  $("forget").onclick = async () => { await api("POST", "/api/forget", {}); alert("Saved settings removed."); };

  $("refresh").onclick = loadDevices;
  $("show-all").onchange = loadDevices;
  $("next-3").onclick = () => showStep(4);
  $("confirm").onchange = () => { $("write").disabled = !$("confirm").checked; };
  $("write").onclick = startWrite;
  document.querySelectorAll(".back").forEach((b) => { b.onclick = () => showStep(Number(b.dataset.to)); });

  $("cancel").onclick = () => { if (confirm("Stop writing? The card will be unusable until written again.")) api("POST", "/api/cancel", {}); };
  $("again").onclick = () => { state.device = null; showStep(3); };
  $("quit").onclick = async () => {
    try { await api("POST", "/api/quit", {}); document.body.replaceChildren(el("main", {}, el("p", {}, "Meteor Imager has quit. You can close this tab."))); }
    catch (e) { alert(e.message); }
  };
  // Keep the app alive while this tab is open.
  setInterval(() => { api("GET", "/api/job").catch(() => {}); }, 20000);
}

function guessCountry() {
  const m = (navigator.language || "").match(/-([A-Z]{2})$/);
  return m ? m[1] : "";
}

init();
