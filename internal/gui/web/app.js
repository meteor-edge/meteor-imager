"use strict";
// Meteor Imager UI. Secrets (Wi-Fi password, enrollment key, user password)
// live only in the form fields and the single /api/flash request; they are
// never stored.

const $ = (id) => document.getElementById(id);
const state = {
  source: "server", // or "file"
  server: null, insecure: false, images: [], imageId: null,
  devices: [], device: null,
  step: 1,
  backupDevices: [], backupDevice: null, backupDir: "", lastBackupPath: "",
  job: "write", // kind of the job the progress page shows
  // From an uploaded meteorenv.txt when the plain password is not in the file.
  importedPasswordHash: "",
};

const USER_RE = /^[a-z_][a-z0-9_-]{0,31}$/;
const SSH_KEY_RE = /^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-\S+) [A-Za-z0-9+/=]+( .*)?$/;

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

function setMode(mode) {
  $("mode-write").setAttribute("aria-selected", mode === "write");
  $("mode-backup").setAttribute("aria-selected", mode === "backup");
  if (mode === "write") { showStep(state.step); return; }
  for (let i = 1; i <= 4; i++) $(`step-${i}`).hidden = true;
  $("steps").hidden = true;
  $("progress").hidden = true;
  $("backup").hidden = false;
  loadBackupDevices();
}

function showStep(n) {
  state.step = n;
  for (let i = 1; i <= 4; i++) $(`step-${i}`).hidden = i !== n;
  $("steps").hidden = false;
  $("backup").hidden = true;
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
const PASSWORD_ALPHABET =
  "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789!@#$%^&*";

/** Random password for user / Wi-Fi fields (browser crypto only). */
function generatePassword(length) {
  const size = Math.max(8, Math.min(length || 16, 63));
  const bytes = new Uint8Array(size);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (b) => PASSWORD_ALPHABET[b % PASSWORD_ALPHABET.length]).join("");
}

function setSettingsTab(tab) {
  $("tab-general").setAttribute("aria-selected", tab === "general");
  $("tab-meteor").setAttribute("aria-selected", tab === "meteor");
  $("pane-general").hidden = tab !== "general";
  $("pane-meteor").hidden = tab !== "meteor";
}

function buildProfile() {
  const p = { schema_version: 3, hostname: null, timezone: null };
  if ($("use-enroll").checked) {
    p.enrollment = { meteor_domain: $("domain").value.trim(), meteor_api_key: $("api-key").value.trim() };
    const name = $("device-name").value.trim();
    if (name) p.enrollment.device_name = name;
  }
  if ($("use-wifi").checked) {
    p.wifi = { ssid: $("ssid").value, psk: $("psk").value, hidden: $("hidden-ssid").checked, country: $("country").value.trim().toUpperCase() };
  }
  const host = $("hostname").value.trim();
  const tz = $("timezone").value.trim();
  if (host) p.hostname = host;
  if (tz) p.timezone = tz;
  // SSH access: username/password live with the SSH section.
  if ($("use-ssh").checked && $("use-user").checked) {
    p.user = { name: $("user-name").value.trim(), password: $("user-password").value };
    if (!p.user.password && state.importedPasswordHash) {
      p.user.password_hash = state.importedPasswordHash;
    }
  }
  if ($("use-ssh").checked) {
    p.ssh = { enabled: true, password_authentication: $("ssh-auth-password").checked };
    const key = $("ssh-key").value.trim();
    if (key) p.ssh.authorized_keys = [key];
  }
  return p;
}

/** Parse KEY=value meteorenv / .meteorenv text (same format the console downloads). */
function parseMeteorenv(text) {
  const out = {};
  const cleaned = text.replace(/^\uFEFF/, "");
  for (const raw of cleaned.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const eq = line.indexOf("=");
    if (eq <= 0) continue;
    const key = line.slice(0, eq).trim();
    let value = line.slice(eq + 1);
    if ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'"))) {
      value = value.slice(1, -1);
    }
    out[key] = value;
  }
  return out;
}

function truthy(v) {
  return /^(1|true|yes|on)$/i.test(String(v || "").trim());
}

function applyMeteorenv(map) {
  const notes = [];
  if (map.METEOR_HOSTNAME) $("hostname").value = map.METEOR_HOSTNAME;
  if (map.METEOR_TIMEZONE) $("timezone").value = map.METEOR_TIMEZONE;

  if (map.METEOR_WIFI_SSID || map.METEOR_WIFI_PSK) {
    $("use-wifi").checked = true;
    if (map.METEOR_WIFI_SSID) $("ssid").value = map.METEOR_WIFI_SSID;
    if (map.METEOR_WIFI_PSK) {
      $("psk").value = map.METEOR_WIFI_PSK;
      $("psk").type = "text";
      const reveal = document.querySelector('.reveal[data-field="psk"]');
      if (reveal) reveal.textContent = "Hide";
    }
    if (map.METEOR_WIFI_COUNTRY) $("country").value = map.METEOR_WIFI_COUNTRY.toUpperCase();
    if (map.METEOR_WIFI_HIDDEN != null) $("hidden-ssid").checked = truthy(map.METEOR_WIFI_HIDDEN);
  }

  const sshOn = map.METEOR_SSH_ENABLED == null || truthy(map.METEOR_SSH_ENABLED);
  const hasUser = !!(map.METEOR_USER || map.METEOR_USER_PASSWORD_HASH || map.METEOR_SSH_AUTHORIZED_KEY);
  if (sshOn || hasUser) {
    $("use-ssh").checked = true;
    if (map.METEOR_SSH_PASSWORD_AUTH != null) {
      $(truthy(map.METEOR_SSH_PASSWORD_AUTH) ? "ssh-auth-password" : "ssh-auth-key").checked = true;
    }
    if (map.METEOR_SSH_AUTHORIZED_KEY) $("ssh-key").value = map.METEOR_SSH_AUTHORIZED_KEY;
  }
  if (map.METEOR_USER || map.METEOR_USER_PASSWORD_HASH) {
    $("use-user").checked = true;
    if (map.METEOR_USER) $("user-name").value = map.METEOR_USER;
    state.importedPasswordHash = "";
    $("user-password").value = "";
    if (map.METEOR_USER_PASSWORD_HASH) {
      state.importedPasswordHash = map.METEOR_USER_PASSWORD_HASH;
      notes.push("SSH password was a hash in the file; it will be written as-is unless you Generate or type a new one.");
    }
  }

  if (map.METEOR_DOMAIN || map.METEOR_API_KEY) {
    $("use-enroll").checked = true;
    if (map.METEOR_DOMAIN) {
      $("domain").value = map.METEOR_DOMAIN;
      $("domain").dataset.auto = "0";
    }
    if (map.METEOR_API_KEY) {
      $("api-key").value = map.METEOR_API_KEY;
      $("api-key").type = "text";
      const reveal = document.querySelector('.reveal[data-field="api-key"]');
      if (reveal) reveal.textContent = "Hide";
    }
    if (map.METEOR_NAME) $("device-name").value = map.METEOR_NAME;
  }

  syncSettingsFields();
  updatePasswordHashNote();
  const status = $("import-status");
  status.hidden = false;
  status.textContent = notes.length
    ? "Settings loaded. " + notes.join(" ")
    : "Settings loaded into the form.";
  // Open MeteorCloud tab when enrollment was in the file.
  if (map.METEOR_DOMAIN || map.METEOR_API_KEY) setSettingsTab("meteor");
  else setSettingsTab("general");
}

function updatePasswordHashNote() {
  const note = $("password-hash-note");
  note.hidden = !state.importedPasswordHash || !!$("user-password").value;
}

// Problems are tagged with their tab so the form can open it.
function checkSettings() {
  const problems = [];
  const p = buildProfile();
  if (!p.enrollment && !p.wifi && !p.hostname && !p.timezone && !p.user && !p.ssh) {
    return { profile: null, problems }; // write the image unchanged
  }
  const general = (t) => problems.push({ tab: "general", text: t });
  if (p.enrollment) {
    if (!p.enrollment.meteor_domain) problems.push({ tab: "meteor", text: "Enter the MeteorCloud domain." });
    if (!/^key_\S{4,}$/.test(p.enrollment.meteor_api_key)) problems.push({ tab: "meteor", text: "Enter an enrollment key (starts with key_)." });
  }
  if (p.wifi) {
    if (!p.wifi.ssid) general("Enter the Wi-Fi network name.");
    if (p.wifi.psk.length < 8 || p.wifi.psk.length > 63) general("The Wi-Fi password must be 8 to 63 characters.");
    if (!/^[A-Z]{2}$/.test(p.wifi.country)) general("Enter the two-letter Wi-Fi country code, e.g. DE.");
  }
  if (p.user) {
    if (!USER_RE.test(p.user.name) || p.user.name === "root") general("The SSH username must be lower-case letters, digits, - or _ (not root).");
    if (!p.user.password && !p.user.password_hash) general("Enter or Generate an SSH password for the user.");
  }
  if (p.ssh) {
    const key = p.ssh.authorized_keys && p.ssh.authorized_keys[0];
    if (!p.ssh.password_authentication && !key) general("Public-key authentication needs an SSH public key.");
    if (key && !SSH_KEY_RE.test(key)) general("The SSH key must be an OpenSSH public key (ssh-ed25519 AAAA… comment).");
  }
  return { profile: p, problems };
}

function showProblems(problems) {
  const ul = $("problems");
  ul.hidden = !problems.length;
  ul.replaceChildren(...problems.map((p) => el("li", {}, typeof p === "string" ? p : p.text)));
  const first = problems.find((p) => p.tab);
  if (first) setSettingsTab(first.tab);
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
  $("next-3").disabled = !state.device;
  renderDeviceList($("devices"), "device", state.devices, state.device, (d) => { state.device = d; renderDevices(); });
}

function renderDeviceList(list, name, devices, current, onSelect) {
  if (!devices.length) {
    list.replaceChildren(el("div", { class: "empty" }, "No card found. Insert an SD card or USB reader, then press Refresh."));
    return;
  }
  list.replaceChildren(...devices.map(({ device: d, size }) => {
    const selected = current && current.path === d.path;
    const notes = [];
    if (!d.removable) notes.push("not removable");
    if (d.read_only) notes.push("read-only (slide the lock switch)");
    if (d.mountpoints && d.mountpoints.length) notes.push("mounted at " + d.mountpoints.join(", "));
    return el("label", { class: "item" + (selected ? " selected" : "") },
      el("input", { type: "radio", name, value: d.path, checked: selected, disabled: d.read_only,
        onchange: () => onSelect(d) }),
      el("div", {},
        el("div", { class: "title" }, `${d.model || "Unknown device"} `, el("span", { class: "muted" }, size)),
        el("div", { class: "meta mono" }, d.path + (d.bus ? ` · ${d.bus}` : "")),
        notes.length ? el("div", { class: "meta" }, notes.join(" · ")) : null));
  }));
}

// ---------- Back up ----------
async function loadBackupDevices() {
  const list = $("backup-devices");
  list.replaceChildren(el("div", { class: "empty" }, "Looking for cards…"));
  try {
    state.backupDevices = await api("GET", "/api/devices" + ($("backup-show-all").checked ? "?all=1" : ""));
  } catch (e) {
    list.replaceChildren(el("div", { class: "error" }, e.message));
    return;
  }
  const cur = state.backupDevice;
  if (cur && !state.backupDevices.some((d) => d.device.path === cur.path)) state.backupDevice = null;
  renderBackupDevices();
}

function renderBackupDevices() {
  renderDeviceList($("backup-devices"), "backup-device", state.backupDevices, state.backupDevice,
    (d) => { state.backupDevice = d; renderBackupDevices(); });
  $("backup-start").disabled = !state.backupDevice || !$("backup-out").value.trim();
}

function defaultBackupPath() {
  const day = new Date().toISOString().slice(0, 10);
  const sep = state.backupDir.includes("\\") ? "\\" : "/";
  return state.backupDir ? `${state.backupDir}${sep}meteor-image-${day}.img.gz` : "";
}

async function startBackup() {
  const d = state.backupDevice;
  $("backup-start").disabled = true;
  try {
    await api("POST", "/api/backup", {
      device: d.path,
      out: $("backup-out").value.trim(),
      shrink: $("backup-shrink").checked,
      allow_non_removable: !d.removable,
    });
  } catch (e) {
    alert(e.message);
    $("backup-start").disabled = false;
    return;
  }
  showProgress("backup");
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
  if (profile && profile.enrollment && profile.enrollment.device_name) rows.push(["Device name", profile.enrollment.device_name]);
  if (profile && profile.user) {
    const secret = profile.user.password || profile.user.password_hash ? "password ••••••••" : "no password";
    rows.push(["SSH user", `${profile.user.name}, ${secret}`]);
  }
  if (profile && profile.ssh) {
    const key = profile.ssh.authorized_keys ? ", key " + profile.ssh.authorized_keys[0].split(" ").slice(0, 2).join(" ").slice(0, 32) + "…" : "";
    rows.push(["SSH", (profile.ssh.password_authentication ? "on, password or key" : "on, key only") + key]);
  }
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
  state.sshLogin = profile && profile.ssh && profile.ssh.password_authentication && profile.user
    ? `ssh ${profile.user.name}@${profile.hostname ? profile.hostname + ".local" : "<device IP>"}` : "";
  try {
    await api("POST", "/api/flash", req);
  } catch (e) {
    if (e.problems) { showStep(2); showProblems(e.problems); return; }
    alert(e.message);
    $("write").disabled = false;
    return;
  }
  showProgress("write");
}

const STAGES = {
  write: [
    ["download", "Download"], ["open", "Prepare card"], ["write", "Write"], ["verify", "Verify"],
    ["configure", "Settings"], ["finish", "Finish"], ["eject", "Eject"],
  ],
  backup: [["open", "Prepare card"], ["read", "Read"], ["shrink", "Shrink"], ["save", "Save"], ["eject", "Eject"]],
};

function showProgress(kind) {
  state.job = kind;
  for (let i = 1; i <= 4; i++) $(`step-${i}`).hidden = true;
  $("backup").hidden = true;
  $("progress").hidden = false;
  $("result").replaceChildren();
  $("cancel").hidden = false;
  $("again").hidden = true;
  $("start-over").hidden = true;
  $("write-backup").hidden = true;
  $("again").textContent = kind === "backup" ? "Back up another card" : "Write another card";
  $("progress-title").textContent = kind === "backup" ? "Backing up…" : "Writing…";
  poll();
}

async function poll() {
  let st;
  try { st = await api("GET", "/api/job"); } catch (_) { setTimeout(poll, 1000); return; }
  const kind = st.kind || state.job;
  const stages = STAGES[kind];
  $("progress-device").textContent = st.device || "";
  const idx = stages.findIndex(([k]) => k === st.stage);
  $("stages").replaceChildren(...stages
    .filter(([k]) => k !== "download" || state.source === "server")
    .map(([k, label]) => {
      const si = stages.findIndex(([x]) => x === k);
      const cls = st.stage === "done" || si < idx ? "done" : si === idx ? "current" : "";
      return el("li", { class: cls }, label);
    }));
  const pct = Math.round((st.fraction || 0) * 100);
  $("bar-fill").style.width = `${st.stage === "done" ? 100 : pct}%`;
  const label = (stages.find(([k]) => k === st.stage) || [null, ""])[1];
  const verb = { write: "written", verify: "checked", read: "read", save: "saved" }[st.stage];
  const amount = st.bytes && verb ? ` · ${fmtSize(st.bytes)} ${verb}` : "";
  $("progress-label").textContent = st.running ? `${label} ${pct}%${amount}` : "";
  $("messages").replaceChildren(...(st.messages || []).map((m) => el("li", {}, m)));

  if (st.running) { setTimeout(poll, 500); return; }
  $("cancel").hidden = true;
  $("again").hidden = false;
  $("start-over").hidden = false;
  if (st.error) {
    $("progress-title").textContent = kind === "backup" ? "Backup failed" : "Writing failed";
    $("result").replaceChildren(el("div", { class: "error" }, st.error));
    return;
  }
  $("progress-title").textContent = "Done";
  if (kind === "backup") { renderBackupResult(st.backup || {}); return; }
  const r = st.report || {};
  const lines = [el("p", { class: "success" }, `Card written${r.verified ? " and verified" : ""}. You can remove it now.`)];
  if (r.boot && r.boot.written && r.boot.written.length) lines.push(el("p", { class: "muted" }, "Boot partition: " + r.boot.written.join(", ")));
  if (state.sshLogin) lines.push(el("p", {}, "After first boot, log in with the user’s password: ", el("span", { class: "mono" }, state.sshLogin)));
  for (const w of (r.boot && r.boot.warnings) || []) lines.push(el("p", { class: "banner warn" }, w));
  $("result").replaceChildren(...lines);
}

function renderBackupResult(r) {
  state.lastBackupPath = r.path || "";
  $("write-backup").hidden = !state.lastBackupPath;
  const lines = [
    el("p", { class: "success" }, "Backup saved. You can remove the card now."),
    el("dl", { class: "summary" },
      el("dt", {}, "File"), el("dd", { class: "mono" }, r.path),
      el("dt", {}, "Size"), el("dd", {}, `${fmtSize(r.file_bytes)} (image ${fmtSize(r.image_bytes)}${r.shrunk ? ", shrunk" : ""})`),
      el("dt", {}, "SHA-256"), el("dd", { class: "mono" }, r.sha256)),
    el("p", { class: "muted" }, "Next: write cards from it now with “Write this backup to a card”, or share it: in MeteorCloud, open Artifacts, upload this file as an OS image and tick “Show in Meteor Imager”."),
  ];
  for (const n of r.notes || []) lines.push(el("p", { class: "banner warn" }, n));
  $("result").replaceChildren(...lines);
}

// Write the backup that just finished: same flow as a local image file.
function writeBackup() {
  state.device = null;
  $("file-path").value = state.lastBackupPath;
  setSource("file");
  state.step = 2;
  setSettingsTab("general");
  setMode("write");
}

// Back to step 1 with an empty form, as when the app starts (saved preferences included).
async function startOver() {
  let prefs = {};
  try { prefs = (await api("GET", "/api/state")).prefs || {}; } catch (_) { /* keep the form empty */ }
  document.querySelectorAll("#step-1 input, #step-2 input, #step-2 textarea, #step-3 input, #step-4 input").forEach((e) => {
    if (e.type === "checkbox" || e.type === "radio") e.checked = e.defaultChecked;
    else e.value = e.defaultValue;
  });
  document.querySelectorAll(".reveal").forEach((b) => {
    const input = $(b.getAttribute("data-field"));
    if (input) input.type = "password";
    b.textContent = "Show";
  });
  $("server").value = state.server || prefs.server_url || "";
  $("insecure").checked = state.insecure;
  applyPrefs(prefs);
  // Refilled from the server, like a fresh start.
  $("domain").dataset.auto = $("domain").value ? "0" : "1";
  syncSettingsFields();
  showProblems([]);
  setSettingsTab("general");
  Object.assign(state, { imageId: null, device: null, devices: [], images: [], importedPasswordHash: "" });
  const importStatus = $("import-status");
  if (importStatus) { importStatus.hidden = true; importStatus.textContent = ""; }
  setSource("server");
  if ($("server").value) loadImages();
  else $("images").replaceChildren();
  state.step = 1;
  setMode("write");
}

// Non-secret saved preferences into the settings form.
function applyPrefs(p) {
  if (p.meteor_domain) $("domain").value = p.meteor_domain;
  $("ssid").value = p.wifi_ssid || "";
  $("hidden-ssid").checked = !!p.wifi_hidden;
  $("country").value = p.wifi_country || guessCountry();
  $("hostname").value = p.hostname || "";
  $("timezone").value = p.timezone || "";
  // Saved username turns the section on; otherwise keep HTML defaults (user on, "pi").
  if (p.user_name) {
    $("user-name").value = p.user_name;
    $("use-user").checked = true;
  }
  // SSH with password login is on unless it was turned off last time.
  $("use-ssh").checked = p.ssh_enabled !== false;
  $(p.ssh_password_authentication === false ? "ssh-auth-key" : "ssh-auth-password").checked = true;
  $("ssh-key").value = p.ssh_authorized_key || "";
}

// Show only the fields of the ticked sections.
function syncSettingsFields() {
  for (const [box, fields] of [["use-enroll", "enroll-fields"], ["use-wifi", "wifi-fields"],
    ["use-ssh", "ssh-fields"]]) {
    $(fields).hidden = !$(box).checked;
  }
  $("user-fields").hidden = !$("use-ssh").checked || !$("use-user").checked;
  $("ssh-key-optional").hidden = $("ssh-auth-key").checked;
  updatePasswordHashNote();
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
  applyPrefs(p);
  state.backupDir = st.backup_dir || "";
  $("backup-out").value = defaultBackupPath();
  if (!st.shrink_available) {
    $("backup-shrink").checked = false;
    $("backup-shrink").disabled = true;
    $("backup-shrink-hint").textContent = "Shrinking needs e2fsprogs (e2fsck, resize2fs): apt install e2fsprogs, or brew install e2fsprogs on macOS. Without it the image is as large as the card’s partitions.";
  }
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

  for (const id of ["use-enroll", "use-wifi", "use-user", "use-ssh", "ssh-auth-key", "ssh-auth-password"]) {
    $(id).onchange = syncSettingsFields;
  }
  syncSettingsFields();
  $("tab-general").onclick = () => setSettingsTab("general");
  $("tab-meteor").onclick = () => setSettingsTab("meteor");
  $("user-password").addEventListener("input", () => {
    if ($("user-password").value) state.importedPasswordHash = "";
    updatePasswordHashNote();
  });
  $("import-meteorenv").addEventListener("change", async (event) => {
    const file = event.target.files && event.target.files[0];
    event.target.value = "";
    if (!file) return;
    try {
      const text = await file.text();
      const map = parseMeteorenv(text);
      if (!Object.keys(map).length) {
        $("import-status").hidden = false;
        $("import-status").textContent = "That file had no KEY=value settings.";
        return;
      }
      applyMeteorenv(map);
    } catch (e) {
      $("import-status").hidden = false;
      $("import-status").textContent = e.message || "Could not read that file.";
    }
  });
  $("mode-write").onclick = () => setMode("write");
  $("mode-backup").onclick = () => setMode("backup");
  $("backup-refresh").onclick = loadBackupDevices;
  $("backup-show-all").onchange = loadBackupDevices;
  $("backup-out").addEventListener("input", renderBackupDevices);
  $("backup-start").onclick = startBackup;
  // data-field (not data-for): dataset.for is unreliable in some browsers.
  document.querySelectorAll(".reveal").forEach((b) => {
    b.onclick = (event) => {
      event.preventDefault();
      const id = b.getAttribute("data-field");
      const input = id && $(id);
      if (!input) return;
      input.type = input.type === "password" ? "text" : "password";
      b.textContent = input.type === "password" ? "Show" : "Hide";
    };
  });
  document.querySelectorAll(".generate").forEach((b) => {
    b.onclick = (event) => {
      event.preventDefault();
      const id = b.getAttribute("data-field");
      const input = id && $(id);
      if (!input) return;
      input.value = generatePassword(16);
      input.type = "text";
      input.focus();
      const reveal = document.querySelector(`.reveal[data-field="${id}"]`);
      if (reveal) reveal.textContent = "Hide";
      if (id === "user-password") {
        state.importedPasswordHash = "";
        updatePasswordHashNote();
      }
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

  $("cancel").onclick = () => {
    const msg = state.job === "backup" ? "Stop the backup? No file is saved." : "Stop writing? The card will be unusable until written again.";
    if (confirm(msg)) api("POST", "/api/cancel", {});
  };
  $("again").onclick = () => {
    if (state.job === "backup") {
      state.backupDevice = null;
      $("backup-out").value = defaultBackupPath();
      setMode("backup");
      return;
    }
    state.device = null;
    showStep(3);
  };
  $("write-backup").onclick = writeBackup;
  $("start-over").onclick = startOver;
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
