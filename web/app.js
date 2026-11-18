"use strict";

// ---------------------------------------------------------------------------
// API
// ---------------------------------------------------------------------------
const TOKEN = document.querySelector('meta[name="sm-token"]').content;

class ApiError extends Error {
  constructor(status, body) {
    super((body && body.error) || "Request failed (" + status + ")");
    this.status = status;
    this.body = body || {};
  }
}

async function api(method, path, body) {
  const opts = { method, headers: { "X-SM-Token": TOKEN } };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch(path, opts);
  } catch (e) {
    throw new ApiError(0, { error: "SSHFS Station is not responding. Close this window and start it again." });
  }
  let data = null;
  try { data = await res.json(); } catch (e) { /* empty body */ }
  if (!res.ok) throw new ApiError(res.status, data);
  return data;
}

// ---------------------------------------------------------------------------
// Tiny DOM helper. Text is always set with textContent, never parsed as HTML.
// ---------------------------------------------------------------------------
function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k === "dataset") Object.assign(el.dataset, v);
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "value") el.value = v;
    else if (k === "checked") el.checked = !!v;
    else if (k === "text") el.textContent = v;
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) {
    if (c === null || c === undefined || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}
const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

// Help text with a little inline code. Pieces in `backticks` become <code>.
function rich(text) {
  return text.split(/(`[^`]+`)/).map(p => p.startsWith("`") ? h("code", {}, p.slice(1, -1)) : p);
}

function tip(label, text, cls) {
  return h("span", { class: "tip" },
    h("button", { type: "button", class: "tip-btn " + (cls || ""), tabindex: "0" }, label),
    h("span", { class: "tip-box", role: "tooltip" }, rich(text)));
}

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------
const LETTERS = "ABDEFGHIJKLMNOPQRSTUVWXYZ".split(""); // C: is never offered
const LOCATIONS = [["home", "Home"], ["sdcard", "SD Card"], ["root", "Root"], ["custom", "Custom..."]];

const state = {
  info: null,
  devices: [],            // DeviceView[] from the server
  status: { devices: {}, usedLetters: [] },
  modules: null,
  latest: null,           // module status including the newest releases (online check)
  view: "device",         // device | setup | welcome
  selectedId: null,
  draft: null,            // editable copy of the selected device
  pwDirty: false,         // password field typed into since last save
  touched: new Set(),
  attempted: false,       // an action was tried: show every error
  saveTimer: null,
  saving: null,           // promise of the save in flight
  saveState: "",
  lastResults: {},        // letter -> {state, message} from the last mount/unmount
};

// Original line icons for each kind of hardware (not Valve artwork).
const ICON_PATHS = {
  "deck-lcd":
    '<rect x="1.5" y="7" width="21" height="10.5" rx="3.6"/><rect x="7" y="8.8" width="10" height="6.9" rx="0.8"/>' +
    '<circle cx="4.4" cy="10.2" r="1.15"/><circle cx="19.6" cy="10.2" r="1.15"/>' +
    '<path d="M4.4 13.4v2.2M3.3 14.5h2.2"/><circle cx="19.6" cy="14.5" r="0.45" fill="currentColor"/>',
  "deck-oled":
    '<rect x="1.5" y="7" width="21" height="10.5" rx="3.6"/><rect x="7" y="8.8" width="10" height="6.9" rx="0.8" fill="currentColor" fill-opacity="0.35"/>' +
    '<circle cx="4.4" cy="10.2" r="1.15"/><circle cx="19.6" cy="10.2" r="1.15"/>' +
    '<path d="M4.4 13.4v2.2M3.3 14.5h2.2"/><circle cx="19.6" cy="14.5" r="0.45" fill="currentColor"/>',
  "steam-machine":
    '<rect x="4" y="4.5" width="16" height="15" rx="2.6"/><path d="M7 16.5h7"/><circle cx="17" cy="16.5" r="0.7" fill="currentColor"/>',
  "steam-frame":
    '<path d="M6.3 8h11.4A4.3 4.3 0 0 1 22 12.3v.4a4.3 4.3 0 0 1-4.3 4.3h-2.9l-1.5-2h-2.6l-1.5 2H6.3A4.3 4.3 0 0 1 2 12.7v-.4A4.3 4.3 0 0 1 6.3 8z"/>' +
    '<path d="M7.5 8c1.4-2.6 7.6-2.6 9 0"/>',
  "other":
    '<rect x="3" y="5" width="18" height="14" rx="2.2"/><path d="M7 10l2.5 2L7 14M12 14h4"/>',
  "setup":
    '<path d="M14.7 6.3a4 4 0 0 0-5.3 5.2l-5.6 5.6a1.9 1.9 0 0 0 2.7 2.7l5.6-5.6a4 4 0 0 0 5.2-5.3l-2.5 2.5-2.4-.3-.3-2.4z"/>',
};

function modelIcon(id) {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("fill", "none");
  svg.setAttribute("stroke", "currentColor");
  svg.setAttribute("stroke-width", "1.5");
  svg.setAttribute("stroke-linecap", "round");
  svg.setAttribute("stroke-linejoin", "round");
  svg.setAttribute("class", "model-svg");
  svg.setAttribute("aria-hidden", "true");
  svg.innerHTML = ICON_PATHS[id] || ICON_PATHS.other; // fixed strings from this file only
  return svg;
}

const modelInfo = id => (state.info.models.find(m => m.id === id) || state.info.models[state.info.models.length - 1]);
const selected = () => state.devices.find(d => d.id === state.selectedId) || null;

// ---------------------------------------------------------------------------
// Toasts and modal
// ---------------------------------------------------------------------------
function toast(text, kind) {
  const t = h("div", { class: "toast " + (kind || "") }, text);
  $("#toasts").append(t);
  setTimeout(() => t.remove(), kind === "bad" ? 9000 : 4500);
}

let modalOnClose = null;
function openModal(title, body, buttons, onClose) {
  $("#modal-title").textContent = title;
  $("#modal-body").replaceChildren(body);
  $("#modal-foot").replaceChildren(...(buttons || []));
  $("#modal").hidden = false;
  modalOnClose = onClose || null;
  const first = $("#modal-body button, #modal-body input, #modal-foot .btn-primary");
  if (first) first.focus();
}
function closeModal() {
  $("#modal").hidden = true;
  $("#modal-body").replaceChildren();
  if (modalOnClose) { const f = modalOnClose; modalOnClose = null; f(); }
}
$("#modal-close").addEventListener("click", closeModal);
$("#modal").addEventListener("mousedown", e => { if (e.target.id === "modal") closeModal(); });
document.addEventListener("keydown", e => { if (e.key === "Escape" && !$("#modal").hidden) closeModal(); });

function busy(btn, on) {
  if (!btn) return;
  btn.disabled = on;
  btn.classList.toggle("busy", on);
}

// ---------------------------------------------------------------------------
// Loading
// ---------------------------------------------------------------------------
async function loadDevices() {
  state.devices = await api("GET", "/api/devices");
}

async function loadStatus() {
  try {
    state.status = await api("GET", "/api/status");
  } catch (e) { return; }
  renderSidebar();
  if (state.view === "device") updateDriveStatus();
}

async function loadModules() {
  try {
    state.modules = await api("GET", "/api/modules");
  } catch (e) { return; }
  $("#modules-banner").hidden = state.modules.ready || state.view === "setup";
  updateSetupDot();
}

// Looks up the newest WinFsp and SSHFS-Win releases (the app caches this for an hour).
async function loadLatest(force) {
  try {
    state.latest = await api("GET", "/api/modules?latest=1" + (force ? "&force=1" : ""));
  } catch (e) { return; }
  updateSetupDot();
  if (state.view === "setup") renderModules();
}

function updateSetupDot() {
  const dot = $("#setup-dot");
  const ready = state.modules && state.modules.ready;
  const updates = ready && state.latest && state.latest.ready && state.latest.updatesAvailable;
  dot.className = "dot " + (!ready ? "bad" : updates ? "warn" : "ok");
  dot.title = !ready ? "Components missing" : updates ? "Updates available" : "Ready";
}

// ---------------------------------------------------------------------------
// Sidebar
// ---------------------------------------------------------------------------
function deviceSummary(d) {
  const errors = d.issues.some(i => i.severity === "error");
  const rows = (state.status.devices[d.id] || []).filter(r => r.label !== "Old mapping");
  const mounted = rows.filter(r => r.state === "mounted").length;
  if (rows.some(r => r.stale)) return ["Remount needed", "warn"];
  if (rows.some(r => r.state === "conflict") && mounted === 0) return ["Letter in use", "bad"];
  if (mounted && mounted === rows.length) return [rows.length === 1 ? "Mounted" : "All " + mounted + " mounted", "ok"];
  if (mounted) return [mounted + " of " + rows.length + " mounted", "part"];
  if (errors) return ["Needs info", "warn"];
  return ["Not mounted", ""];
}

function renderSidebar() {
  const list = $("#device-list");
  if (!state.devices.length) {
    list.replaceChildren(h("div", { class: "empty-list" }, "No devices yet. Add your Steam Deck, Steam Machine or Steam Frame."));
  } else {
    list.replaceChildren(...state.devices.map(d => {
      const [label, cls] = deviceSummary(d);
      return h("button", {
        class: "device-item" + (state.view === "device" && d.id === state.selectedId ? " active" : ""),
        onclick: () => selectDevice(d.id),
        title: d.name,
      },
        h("span", { class: "device-icon" }, modelIcon(d.model)),
        h("span", { class: "device-name" }, d.name || "(no name)"),
        h("span", { class: "device-meta" },
          h("span", { class: "pill " + cls }, label),
          h("span", { class: "host" }, d.host || "no address")));
    }));
  }
  $("#nav-setup").classList.toggle("active", state.view === "setup");
}

// ---------------------------------------------------------------------------
// Navigation
// ---------------------------------------------------------------------------
async function selectDevice(id) {
  await flushSave();
  state.view = "device";
  state.selectedId = id;
  const d = selected();
  state.draft = d ? JSON.parse(JSON.stringify(d)) : null;
  state.pwDirty = false;
  state.touched = new Set();
  state.attempted = false;
  state.lastResults = {};
  state.saveState = "";
  $("#modules-banner").hidden = !state.modules || state.modules.ready;
  render();
}

async function showSetup() {
  await flushSave();
  state.view = "setup";
  $("#modules-banner").hidden = true;
  render();
}

function render() {
  renderSidebar();
  const view = $("#view");
  if (state.view === "setup") return renderSetup(view);
  if (!state.devices.length || !state.draft) return renderWelcome(view);
  renderDevice(view);
}

function renderWelcome(view) {
  state.view = "welcome";
  view.replaceChildren(h("div", { class: "welcome" },
    h("h1", {}, "Map your Steam hardware as Windows drives"),
    h("p", {}, "Add a Steam Deck (LCD or OLED), Steam Machine or Steam Frame. Each one gets its own settings and drive letters, and you can add as many as you own."),
    h("button", { class: "btn btn-primary", onclick: pickModel }, "+ Add your first device")));
}

// ---------------------------------------------------------------------------
// Add device
// ---------------------------------------------------------------------------
function pickModel() {
  const create = async (model) => {
    closeModal();
    try {
      await flushSave();
      const d = await api("POST", "/api/devices", { model });
      await loadDevices();
      await loadStatus();
      await selectDevice(d.id);
      const host = $("#f-host");
      if (host && !host.value) host.focus(); else { const n = $("#f-name"); if (n) n.select(); }
    } catch (e) { toast(e.message, "bad"); }
  };
  const main = state.info.models.filter(m => m.id !== "other");
  const other = state.info.models.find(m => m.id === "other");
  openModal("Add a device", h("div", {},
    h("div", { class: "model-grid" }, main.map(m =>
      h("button", { class: "model-card", onclick: () => create(m.id) },
        h("span", { class: "mi" }, modelIcon(m.id)),
        h("span", {}, h("div", { class: "ml" }, m.label),
          h("div", { class: "sub" }, m.defaultHost ? "Starts as " + m.defaultHost : "Enter its name or IP"))))),
    other ? h("div", { class: "model-other" },
      h("button", { class: "link-btn", onclick: () => create(other.id) }, "Something else that runs SSH")) : null));
}

// ---------------------------------------------------------------------------
// Saving (automatic)
// ---------------------------------------------------------------------------
function scheduleSave() {
  state.saveState = "Unsaved changes";
  updateSaveState();
  clearTimeout(state.saveTimer);
  state.saveTimer = setTimeout(() => { state.saveTimer = null; doSave(); }, 600);
}

async function flushSave() {
  if (state.saveTimer) {
    clearTimeout(state.saveTimer);
    state.saveTimer = null;
    await doSave();
  } else if (state.saving) {
    await state.saving.catch(() => {});
  }
}

function doSave() {
  const draft = state.draft;
  if (!draft) return Promise.resolve();
  const body = { device: stripView(draft) };
  if (state.pwDirty) body.password = $("#f-password") ? $("#f-password").value : "";
  const id = draft.id;
  state.saveState = "Saving...";
  updateSaveState();
  const p = (async () => {
    try {
      const v = await api("PUT", "/api/devices/" + id, body);
      if (body.password !== undefined) state.pwDirty = false;
      const i = state.devices.findIndex(d => d.id === id);
      if (i >= 0) state.devices[i] = v;
      if (state.draft && state.draft.id === id) {
        state.draft.issues = v.issues;
        state.draft.hasPassword = v.hasPassword;
        state.saveState = "All changes saved";
        updateIssues();
        updatePasswordField();
      }
      renderSidebar();
    } catch (e) {
      state.saveState = "Not saved";
      toast("Could not save: " + e.message, "bad");
    } finally {
      updateSaveState();
    }
  })();
  state.saving = p;
  return p;
}

function stripView(d) {
  const { hasPassword, issues, ...dev } = d;
  dev.port = parseInt(dev.port, 10) || 0;
  return dev;
}

function updateSaveState() {
  const el = $("#save-state");
  if (el) el.textContent = state.saveState;
}

// ---------------------------------------------------------------------------
// Device view
// ---------------------------------------------------------------------------
function field(key, labelChildren, control, extra) {
  return h("div", { class: "field", dataset: { field: key } },
    h("label", { class: "label", for: control.id }, labelChildren),
    control,
    extra || null,
    h("div", { class: "msg", dataset: { msgFor: key }, hidden: true }));
}

function bindText(input, key, opts = {}) {
  input.addEventListener("input", () => {
    state.draft[key] = opts.number ? input.value : input.value;
    if (opts.liveTouched) state.touched.add(key);
    if (opts.after) opts.after();
    scheduleSave();
  });
  input.addEventListener("blur", () => { state.touched.add(key); updateIssues(); });
}

function renderDevice(view) {
  const d = state.draft;
  const mi = modelInfo(d.model);

  const nameInput = h("input", { class: "input", id: "f-name", value: d.name, maxlength: "60", autocomplete: "off", spellcheck: "false" });
  bindText(nameInput, "name", { after: () => { $("#dev-title").textContent = state.draft.name || "(no name)"; updateCmdline(); } });

  const modelSel = h("select", { class: "select", id: "f-model" },
    state.info.models.map(m => h("option", { value: m.id, selected: m.id === d.model }, m.label)));
  modelSel.addEventListener("change", async () => {
    const oldUser = modelInfo(state.draft.model).defaultUser || "deck";
    state.draft.model = modelSel.value;
    // Still on the old hardware's default user? Switch to the new one (deck -> steamos for a Steam Frame).
    const cur = (state.draft.username || "").trim();
    const newUser = modelInfo(modelSel.value).defaultUser;
    if (newUser && (cur === "" || cur === oldUser) && cur !== newUser) {
      state.draft.username = newUser;
      toast("Username changed to " + newUser + ", the default on a " + modelInfo(modelSel.value).label + ".", "ok");
    }
    scheduleSave();
    await flushSave();
    renderDevice($("#view")); // tips and placeholders depend on the hardware
  });

  const hostInput = h("input", { class: "input", id: "f-host", value: d.host, autocomplete: "off", spellcheck: "false", placeholder: mi.defaultHost || "e.g. 192.168.1.50" });
  bindText(hostInput, "host", { after: updateDriveUNCs });

  const portInput = h("input", { class: "input", id: "f-port", type: "number", min: "1", max: "65535", value: d.port ? String(d.port) : "", placeholder: "22" });
  bindText(portInput, "port", { number: true, after: updateDriveUNCs });

  const defUser = mi.defaultUser || "deck";
  const userInput = h("input", { class: "input", id: "f-username", value: d.username, autocomplete: "off", spellcheck: "false", placeholder: defUser });
  bindText(userInput, "username", { after: updateDriveUNCs });

  // A text box drawn with dots instead of type="password": browsers' password
  // managers only pop up for real password fields.
  const pwInput = h("input", {
    class: "input secret", id: "f-password", type: "text", autocomplete: "off", spellcheck: "false",
    autocapitalize: "off", "data-lpignore": "true", "data-1p-ignore": "true", "data-form-type": "other",
  });
  pwInput.addEventListener("input", () => { state.pwDirty = true; scheduleSave(); });
  pwInput.addEventListener("blur", () => { state.touched.add("password"); updateIssues(); });
  const pwShow = h("button", { type: "button", class: "btn btn-small", onclick: () => {
    const shown = pwInput.classList.toggle("revealed");
    pwShow.textContent = shown ? "Hide" : "Show";
  } }, "Show");
  const pwForget = h("button", { type: "button", class: "link-btn", id: "pw-forget", onclick: () => {
    pwInput.value = "";
    state.pwDirty = true;
    scheduleSave();
  } }, "Forget saved password");

  const secure = state.info.secureSecrets;
  const safeText = secure
    ? "Yes. It is saved encrypted with your Windows account (DPAPI), so only you on this PC can read it, and it is never sent anywhere except to your device when mounting."
    : "In demo mode the password is only kept in memory and never sent anywhere.";

  const sdInput = h("input", { class: "input", id: "f-sdCard", value: d.sdCard, autocomplete: "off", spellcheck: "false", placeholder: "e.g. sdcard", disabled: d.noSd });
  bindText(sdInput, "sdCard", { after: () => { renderDrives(); } });
  const noSd = h("input", { type: "checkbox", id: "f-noSd", checked: d.noSd });
  noSd.addEventListener("change", () => {
    state.draft.noSd = noSd.checked;
    sdInput.disabled = noSd.checked;
    $("#sd-hint").textContent = sdHint();
    renderDrives();
    scheduleSave();
  });

  const autoMount = h("input", { type: "checkbox", id: "f-autoMount", checked: d.autoMount });
  autoMount.addEventListener("change", () => { state.draft.autoMount = autoMount.checked; scheduleSave(); });
  const noPauses = h("input", { type: "checkbox", id: "f-noPauses", checked: d.noPauses });
  noPauses.addEventListener("change", () => { state.draft.noPauses = noPauses.checked; scheduleSave(); });

  view.replaceChildren(
    h("div", { class: "page-head" },
      h("div", { class: "page-title" },
        h("div", { class: "page-icon", id: "dev-icon" }, modelIcon(d.model)),
        h("div", {},
          h("h1", { id: "dev-title" }, d.name || "(no name)"),
          h("div", { class: "sub", id: "dev-model" }, mi.label))),
      h("div", { class: "head-side" },
        h("div", { class: "save-state", id: "save-state" }, state.saveState),
        h("button", { class: "btn btn-small", onclick: e => doDuplicate(e.currentTarget), title: "Make a copy of this profile" }, "Duplicate"),
        h("button", { class: "btn btn-small btn-danger", onclick: doDelete }, "Delete"))),

    h("div", { class: "actions" },
      h("button", { class: "btn btn-primary", id: "act-mount", onclick: e => doMount(e.currentTarget) }, "Mount all"),
      h("button", { class: "btn", id: "act-unmount", onclick: e => doUnmount(e.currentTarget) }, "Unmount all"),
      h("button", { class: "btn", onclick: e => doTest(e.currentTarget) }, "Test connection"),
      h("button", { class: "btn", onclick: e => doExport(e.currentTarget) }, "Export .bat"),
      h("button", { class: "btn", onclick: e => doShortcut(e.currentTarget) }, "Desktop shortcut")),

    h("section", { class: "card" },
      h("div", { class: "card-title" }, "Connection"),
      h("div", { class: "grid2" },
        field("name", ["Profile name", h("span", { class: "req" }, "*"),
          tip("? What is this", "Your own name for this device, like `Living room Deck`. You also use it to mount from the command line or a shortcut.")], nameInput),
        field("model", ["Hardware"], modelSel)),
      h("div", { class: "grid-host" },
        field("host", ["Device name or IP address", h("span", { class: "req" }, "*"), tip("? How to find", mi.hostHint + " On the device, Desktop Mode > Konsole: `hostname` shows the name and `ip -4 addr` shows the IP address.")], hostInput),
        field("port", ["SSH port"], portInput)),
      h("div", { class: "grid2" },
        field("username", ["Username", h("span", { class: "req" }, "*"),
          tip("? How to find", "`" + defUser + "` is the default on a " + mi.label + ", so use that unless you know you have changed it. In Konsole, `whoami` shows it.")], userInput),
        field("password", ["SUDO password", h("span", { class: "req" }, "*"),
          tip("? How to get one", "You must set a password on the device first. In Desktop Mode open Konsole and type `passwd`. SSH must be on too: `sudo systemctl enable --now sshd`."),
          tip("Is this safe?", safeText, "safe")],
          h("div", { class: "pw-wrap" }, pwInput, pwShow),
          h("div", { class: "hint", id: "pw-hint" }, "", pwForget)))),

    h("section", { class: "card" },
      h("div", { class: "card-title" }, "SD card"),
      field("sdCard", ["SD card drive name",
        tip("? How to find", "Mount the Root drive first and open `run/media/" + (d.username || "deck") + "` on it. The folder in there is your SD card's name. Bring the name back here. You can leave this blank until you know it.")],
        sdInput,
        h("div", {},
          h("label", { class: "check" }, noSd, "I don't have an SD card"),
          h("div", { class: "hint", id: "sd-hint" }, sdHint())))),

    h("section", { class: "card", id: "drives-card" },
      h("div", { class: "card-title" }, "Drive mappings"),
      h("div", { id: "drives" }),
      h("div", { class: "msg", dataset: { msgFor: "drives" }, hidden: true }),
      h("div", { id: "old-mounts" })),

    h("section", { class: "card" },
      h("div", { class: "card-title" }, "Options"),
      h("label", { class: "toggle-row" }, autoMount, h("span", { class: "switch" }),
        h("span", { class: "toggle-text" }, "Mount automatically when I sign in to Windows",
          h("small", {}, "Adds one SSHFS Station entry to your Windows Startup folder. It retries for a little while if the network is slow to come up."))),
      h("label", { class: "toggle-row" }, noPauses, h("span", { class: "switch" }),
        h("span", { class: "toggle-text" }, "Remove pauses from exported .bat files",
          h("small", {}, "For advanced users. Only affects Export .bat.")))),

    h("section", { class: "card" },
      h("div", { class: "card-title" }, "Command line"),
      h("div", { class: "cmdline" },
        h("code", { id: "cmdline" }),
        h("button", { class: "btn btn-small", onclick: () => copyText($("#cmdline").textContent, "Command copied") }, "Copy")),
      h("div", { class: "hint" }, "Mounts this device without opening the window. Add ", h("code", {}, "--quiet"),
        " to skip the confirmation, or use ", h("code", {}, "unmount"), ", ", h("code", {}, "status"), " and ", h("code", {}, "test"),
        ". Use sshfs-station-cli.exe in scripts and terminals.")),
  );

  updatePasswordField();
  updateCmdline();
  renderDrives();
  updateIssues();
}

function sdHint() {
  return state.draft.noSd
    ? "Drives set to SD Card are skipped."
    : "Drives set to SD Card turn on once you enter this name.";
}

function updatePasswordField() {
  const pw = $("#f-password");
  if (!pw || !state.draft) return;
  const has = state.draft.hasPassword;
  pw.placeholder = has ? "Saved (type to change it)" : "Your device's SUDO password";
  const forget = $("#pw-forget");
  if (forget) forget.hidden = !has;
  const hint = $("#pw-hint");
  if (hint && hint.firstChild && hint.firstChild.nodeType === 3) {
    hint.firstChild.textContent = has ? "A password is saved for this device. " : "";
  }
}

function updateCmdline() {
  const el = $("#cmdline");
  if (el) el.textContent = 'sshfs-station.exe "' + (state.draft.name || "") + '"';
}

async function copyText(text, msg) {
  try {
    await navigator.clipboard.writeText(text);
    toast(msg || "Copied", "ok");
  } catch (e) {
    toast("Could not copy to the clipboard", "bad");
  }
}

// ---------------------------------------------------------------------------
// Drives
// ---------------------------------------------------------------------------
function hasSD() {
  return !state.draft.noSd && (state.draft.sdCard || "").trim() !== "";
}

function remotePath(dr) {
  const u = (state.draft.username || "").trim();
  switch (dr.location) {
    case "home": return "/home/" + u;
    case "sdcard": return hasSD() ? "/run/media/" + u + "/" + state.draft.sdCard.trim() : null;
    case "root": return "/";
    case "custom": {
      let p = (dr.custom || "").trim().replace(/\\/g, "/");
      if (!p) return null;
      if (!p.startsWith("/")) p = "/" + p;
      p = p.replace(/\/+/g, "/");
      if (p.length > 1) p = p.replace(/\/+$/, "");
      return p;
    }
  }
  return null;
}

function uncFor(dr) {
  const rp = remotePath(dr);
  if (rp === null) return "";
  const d = state.draft;
  const port = parseInt(d.port, 10) || 22;
  let s = "\\\\sshfs.r\\" + (d.username || "").trim() + "@" + (d.host || "").trim() + (port !== 22 ? "!" + port : "");
  if (rp !== "/") s += rp.replace(/\//g, "\\");
  return s;
}

function letterOwners() {
  // letter -> name of another profile using it
  const owners = {};
  for (const d of state.devices) {
    if (d.id === state.draft.id) continue;
    for (const dr of d.drives || []) {
      if (dr.location === "sdcard" && (d.noSd || !(d.sdCard || "").trim())) continue;
      owners[dr.letter] = owners[dr.letter] || d.name;
    }
  }
  return owners;
}

function letterOptions(current) {
  const owners = letterOwners();
  const mine = new Set((state.status.devices[state.draft.id] || []).filter(r => r.state === "mounted").map(r => r.letter));
  const used = new Set(state.status.usedLetters || []);
  return LETTERS.map(l => {
    let label = l + ":";
    if (owners[l]) label += "  (" + owners[l] + ")";
    else if (used.has(l) && !mine.has(l)) label += "  (in use)";
    return h("option", { value: l, selected: l === current }, label);
  });
}

function nextFreeLetter() {
  const taken = new Set(state.draft.drives.map(d => d.letter));
  const owners = letterOwners();
  const used = new Set(state.status.usedLetters || []);
  const order = "XYZWVUTSRQPONMLKJIHGFED".split("");
  return order.find(l => !taken.has(l) && !owners[l] && !used.has(l)) ||
    order.find(l => !taken.has(l)) || "X";
}

function renderDrives() {
  const box = $("#drives");
  if (!box) return;
  // Keep focus on the same control across a re-render.
  const active = document.activeElement;
  const focusKey = active && active.dataset ? active.dataset.key : null;
  const caret = active && typeof active.selectionStart === "number" ? active.selectionStart : null;

  const drives = state.draft.drives;
  const rows = drives.map((dr, i) => {
    const letter = h("select", { class: "select", dataset: { key: "d" + i + ".letter" }, "aria-label": "Drive letter" }, letterOptions(dr.letter));
    letter.addEventListener("change", () => {
      dr.letter = letter.value;
      state.touched.add("d" + i + ".letter");
      delete state.lastResults[letter.value];
      renderDrives();
      scheduleSave();
    });
    const loc = h("select", { class: "select", dataset: { key: "d" + i + ".location" }, "aria-label": "Location" },
      LOCATIONS.map(([v, l]) => h("option", { value: v, selected: v === dr.location }, l)));
    loc.addEventListener("change", () => {
      dr.location = loc.value;
      state.touched.add("d" + i + ".location");
      renderDrives();
      scheduleSave();
      if (loc.value === "custom") { const c = $('[data-key="d' + i + '.custom"]'); if (c) c.focus(); }
    });
    const custom = h("input", {
      class: "input", value: dr.custom || "", placeholder: "/home/deck/Games", spellcheck: "false",
      dataset: { key: "d" + i + ".custom" }, "aria-label": "Custom path",
    });
    custom.addEventListener("input", () => {
      dr.custom = custom.value;
      $('[data-unc="' + i + '"]').textContent = uncFor(dr) || " ";
      scheduleSave();
    });
    custom.addEventListener("blur", () => { state.touched.add("d" + i + ".custom"); updateIssues(); });
    const customWrap = h("div", { class: "field", style: dr.location === "custom" ? "" : "visibility:hidden" },
      h("div", { class: "label" }, "Custom path"), custom);

    return h("div", { class: "drive", dataset: { drive: i } },
      h("div", { class: "drive-grid" },
        h("div", { class: "field" }, h("div", { class: "label" }, "Drive " + (i + 1)), letter),
        h("div", { class: "field" }, h("div", { class: "label" }, "Location"), loc),
        customWrap),
      h("div", { class: "drive-foot" },
        h("span", { class: "pill", dataset: { pill: i } }, ""),
        h("span", { class: "unc", dataset: { unc: i } }, uncFor(dr) || " "),
        h("span", { class: "drive-buttons" },
          h("button", { class: "btn btn-small", dataset: { act: i }, onclick: e => toggleDrive(i, e.currentTarget) }, "Mount"),
          h("button", { class: "btn btn-small btn-danger drive-remove", onclick: () => removeDrive(i), title: "Remove this mapping" }, "Remove"))),
      h("div", { class: "msg", dataset: { driveMsg: i }, hidden: true }));
  });

  const add = h("button", { class: "add-drive", disabled: drives.length >= state.info.maxDrives, onclick: addDrive },
    drives.length >= state.info.maxDrives ? "Maximum of " + state.info.maxDrives + " drives" : "+ Add drive mapping");
  box.replaceChildren(...rows, add);

  if (focusKey) {
    const el = $('[data-key="' + focusKey + '"]', box);
    if (el) {
      el.focus();
      if (caret !== null && typeof el.setSelectionRange === "function") { try { el.setSelectionRange(caret, caret); } catch (e) { /* select */ } }
    }
  }
  updateDriveStatus();
  updateIssues();
}

function updateDriveUNCs() {
  state.draft.drives.forEach((dr, i) => {
    const el = $('[data-unc="' + i + '"]');
    if (el) el.textContent = uncFor(dr) || " ";
  });
  updateDriveStatus();
}

function addDrive() {
  if (state.draft.drives.length >= state.info.maxDrives) return;
  state.draft.drives.push({ letter: nextFreeLetter(), location: "home", custom: "" });
  renderDrives();
  scheduleSave();
  const i = state.draft.drives.length - 1;
  const el = $('[data-key="d' + i + '.location"]');
  if (el) el.focus();
}

function removeDrive(i) {
  state.draft.drives.splice(i, 1);
  // Drive indexes shift: drop touched marks for drives so stale errors are not shown.
  state.touched = new Set([...state.touched].filter(k => !/^d\d+\./.test(k)));
  renderDrives();
  scheduleSave();
}

// Status pill and Mount/Unmount button for each drive row.
function updateDriveStatus() {
  if (!state.draft) return;
  const rows = state.status.devices[state.draft.id] || [];
  state.draft.drives.forEach((dr, i) => {
    const pill = $('[data-pill="' + i + '"]');
    const btn = $('[data-act="' + i + '"]');
    const row = $('[data-drive="' + i + '"]');
    if (!pill || !btn || !row) return;
    const skipped = dr.location === "sdcard" && !hasSD();
    row.classList.toggle("skipped", skipped);
    const st = rows.find(r => r.letter === dr.letter && r.label !== "Old mapping");
    let text = "Not mounted", cls = "", title = "";
    if (skipped) { text = "Skipped"; cls = "warn"; }
    else if (st && st.state === "mounted" && st.stale) { text = "Mounted (old settings)"; cls = "warn"; title = st.message + " Mapped to " + st.mapped; }
    else if (st && st.state === "mounted") { text = "Mounted"; cls = "ok"; }
    else if (st && st.state === "conflict") { text = "Letter in use"; cls = "bad"; title = st.message; }
    const res = state.lastResults[dr.letter];
    if (res && res.state === "failed" && !(st && st.state === "mounted")) { text = "Failed"; cls = "bad"; }
    pill.textContent = text;
    pill.className = "pill " + cls;
    pill.title = title;
    const mounted = !skipped && st && st.state === "mounted";
    btn.textContent = mounted ? "Unmount" : "Mount";
    btn.disabled = skipped;
    btn.dataset.mounted = mounted ? "1" : "";
  });

  const old = rows.filter(r => r.label === "Old mapping");
  const box = $("#old-mounts");
  if (box) {
    box.replaceChildren(...old.map(r => h("div", { class: "note warn old-mount" },
      h("span", {}, r.letter + ": is still mounted from this profile's earlier settings (" + r.mapped + ")."),
      h("button", { class: "btn btn-small", onclick: e => doUnmount(e.currentTarget, [r.letter]) }, "Unmount " + r.letter + ":"))));
  }
  const anyMounted = rows.some(r => r.state === "mounted");
  const un = $("#act-unmount");
  if (un) un.disabled = !anyMounted;
}

// ---------------------------------------------------------------------------
// Validation display
// ---------------------------------------------------------------------------
const TOP_FIELDS = ["name", "host", "port", "username", "password", "sdCard", "drives"];

function issueVisible(i) {
  if (i.severity === "warn") return true;
  if (state.attempted) return true;
  if (i.drive < 0) return state.touched.has(i.field);
  return state.touched.has("d" + i.drive + "." + i.field) ||
    (i.field === "letter" && state.touched.has("d" + i.drive + ".location"));
}

function currentIssues() {
  return (state.draft && state.draft.issues) || [];
}

function updateIssues() {
  if (!state.draft) return;
  const issues = currentIssues().filter(issueVisible);

  for (const f of TOP_FIELDS) {
    const msg = $('[data-msg-for="' + f + '"]');
    const wrap = $('[data-field="' + f + '"]');
    const is = issues.find(i => i.drive < 0 && i.field === f && i.severity === "error") ||
      issues.find(i => i.drive < 0 && i.field === f);
    if (wrap) wrap.classList.toggle("has-error", !!is && is.severity === "error");
    if (msg) {
      msg.hidden = !is;
      msg.className = "msg " + (is ? is.severity : "");
      msg.textContent = is ? is.message : "";
    }
  }

  state.draft.drives.forEach((dr, i) => {
    const row = $('[data-drive="' + i + '"]');
    const msg = $('[data-drive-msg="' + i + '"]');
    if (!row || !msg) return;
    const mine = issues.filter(x => x.drive === i);
    const errs = mine.filter(x => x.severity === "error");
    row.classList.toggle("has-error", errs.length > 0);
    for (const f of ["letter", "location", "custom"]) {
      const el = $('[data-key="d' + i + "." + f + '"]', row);
      if (el) el.classList.toggle("has-error", errs.some(x => x.field === f));
    }
    const res = state.lastResults[dr.letter];
    const show = errs[0] || mine[0] || (res && res.state === "failed" ? { severity: "error", message: res.message } : null);
    msg.hidden = !show;
    msg.className = "msg " + (show ? show.severity : "");
    msg.textContent = show ? show.message : "";
    if (show && show.message.includes("systemctl enable --now sshd")) {
      msg.append(" ", h("button", { class: "btn btn-small", onclick: e => doTest(e.currentTarget) }, "Test connection"));
    }
  });
}

// Show every problem, scroll to the first one and shake it.
function revealErrors(issues) {
  if (issues) state.draft.issues = issues;
  state.attempted = true;
  updateIssues();
  const first = $(".field.has-error, .drive.has-error");
  if (!first) {
    toast("Fix the highlighted problems first.", "bad");
    return;
  }
  first.scrollIntoView({ behavior: "smooth", block: "center" });
  first.classList.remove("shake");
  void first.offsetWidth;
  first.classList.add("shake");
  first.addEventListener("animationend", () => first.classList.remove("shake"), { once: true });
  const input = $("input:not([disabled]), select:not([disabled])", first);
  if (input) setTimeout(() => input.focus({ preventScroll: true }), 350);
}

// Errors from the server: 422 (problems with the profile) and 412 (setup needed).
function handleActionError(e) {
  if (e.status === 422 && e.body.issues) {
    revealErrors(e.body.issues);
    return;
  }
  if (e.status === 412) {
    toast("WinFsp and SSHFS-Win must be installed first. Opening Setup.", "bad");
    showSetup();
    return;
  }
  toast(e.message, "bad");
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------
function summarize(results, verb) {
  const ok = results.filter(r => r.state !== "failed").map(r => r.letter + ":");
  const bad = results.filter(r => r.state === "failed");
  if (!bad.length) toast((ok.join(" ") || "Nothing") + " " + verb + ".", "ok");
  else toast(bad.length + " of " + results.length + " drives failed:\n" + bad.map(r => r.letter + ": " + r.message).join("\n"), "bad");
}

async function doMount(btn, letters) {
  await flushSave();
  busy(btn, true);
  try {
    const res = await api("POST", "/api/devices/" + state.draft.id + "/mount", { letters: letters || [] });
    for (const r of res) state.lastResults[r.letter] = r;
    summarize(res, "mounted");
  } catch (e) {
    handleActionError(e);
  } finally {
    busy(btn, false);
    await loadStatus();
    updateIssues();
  }
}

async function doUnmount(btn, letters) {
  await flushSave();
  busy(btn, true);
  try {
    const res = await api("POST", "/api/devices/" + state.draft.id + "/unmount", { letters: letters || [] });
    for (const r of res) state.lastResults[r.letter] = r;
    summarize(res, "unmounted");
  } catch (e) {
    handleActionError(e);
  } finally {
    busy(btn, false);
    await loadStatus();
    updateIssues();
  }
}

function toggleDrive(i, btn) {
  const dr = state.draft.drives[i];
  if (btn.dataset.mounted) doUnmount(btn, [dr.letter]);
  else doMount(btn, [dr.letter]);
}

async function doTest(btn) {
  await flushSave();
  if (!(state.draft.host || "").trim()) {
    revealErrors(currentIssues());
    return;
  }
  busy(btn, true);
  try {
    const steps = await api("POST", "/api/devices/" + state.draft.id + "/test");
    const ok = steps.every(s => s.ok);
    openModal("Test connection: " + state.draft.name, h("div", {},
      h("div", { class: "note " + (ok ? "info" : "warn") }, ok
        ? "Your device answered over SSH. If mounting still fails, check the username and password."
        : "The device could not be reached. The first failed step says what to try."),
      h("ul", { class: "steps" }, steps.map(s => h("li", {},
        h("span", { class: "mark " + (s.ok ? "ok" : "bad") }, s.ok ? "✓" : "✗"),
        h("span", {}, s.name),
        h("span", { class: "step-detail" }, s.detail))))),
      [h("button", { class: "btn btn-primary", onclick: closeModal }, "Close")]);
  } catch (e) {
    handleActionError(e);
  } finally {
    busy(btn, false);
  }
}

async function doExport(btn) {
  await flushSave();
  busy(btn, true);
  try {
    const r = await api("GET", "/api/devices/" + state.draft.id + "/export");
    const save = () => {
      const url = URL.createObjectURL(new Blob([r.bat], { type: "application/octet-stream" }));
      const a = h("a", { href: url, download: r.fileName });
      document.body.append(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 2000);
      toast("Saved " + r.fileName + " to your Downloads folder.", "ok");
    };
    openModal("Export .bat: " + state.draft.name, h("div", {},
      h("div", { class: "note warn" }, "This file contains your password in plain text, like the original web tool did. Keep it private. The Mount button and desktop shortcut do not need it."),
      h("pre", { class: "code-block" }, r.bat)),
      [h("button", { class: "btn", onclick: () => copyText(r.bat, "Script copied") }, "Copy"),
        h("button", { class: "btn btn-primary", onclick: save }, "Save .bat")]);
  } catch (e) {
    handleActionError(e);
  } finally {
    busy(btn, false);
  }
}

async function doShortcut(btn) {
  await flushSave();
  busy(btn, true);
  try {
    const r = await api("POST", "/api/devices/" + state.draft.id + "/shortcut");
    toast("Shortcut created:\n" + r.path, "ok");
  } catch (e) {
    handleActionError(e);
  } finally {
    busy(btn, false);
  }
}

async function doDuplicate(btn) {
  await flushSave();
  busy(btn, true);
  try {
    const d = await api("POST", "/api/devices/" + state.draft.id + "/duplicate");
    await loadDevices();
    await selectDevice(d.id);
    toast("Copied. Enter the password for the copy, and change its address if it is a different device.", "ok");
  } catch (e) {
    toast(e.message, "bad");
  } finally {
    busy(btn, false);
  }
}

function doDelete() {
  const d = state.draft;
  const mounted = (state.status.devices[d.id] || []).some(r => r.state === "mounted");
  openModal("Delete " + d.name + "?", h("div", {},
    h("p", {}, "This removes the profile and its saved password from this PC. Nothing on the device is touched."),
    mounted ? h("div", { class: "note warn" }, "Some of its drives are mounted. They will be unmounted first.") : null),
    [h("button", { class: "btn", onclick: closeModal }, "Cancel"),
      h("button", { class: "btn btn-danger", onclick: async (e) => {
        busy(e.currentTarget, true);
        try {
          clearTimeout(state.saveTimer);
          state.saveTimer = null;
          if (mounted) await api("POST", "/api/devices/" + d.id + "/unmount", { letters: [] });
          await api("DELETE", "/api/devices/" + d.id);
          closeModal();
          await loadDevices();
          await loadStatus();
          state.draft = null;
          state.selectedId = null;
          if (state.devices.length) await selectDevice(state.devices[0].id); else render();
          toast("Deleted " + d.name + ".", "ok");
        } catch (err) {
          busy(e.currentTarget, false);
          toast(err.message, "bad");
        }
      } }, "Delete")]);
}

// ---------------------------------------------------------------------------
// Setup and tester
// ---------------------------------------------------------------------------
let installPoll = null;

function moduleCard(m, url, what, checked, latestError) {
  const [label, cls] = !m.installed ? ["Missing", "bad"] : m.updateAvailable ? ["Update available", "warn"] : ["Installed", "ok"];
  let latestLine;
  if (latestError) latestLine = h("p", {}, "Latest: could not check (offline?)");
  else if (!checked) latestLine = h("p", {}, "Latest: checking...");
  else latestLine = h("p", {}, "Latest: " + (m.latest || "unknown"),
    m.releaseUrl ? [" - ", h("a", { href: m.releaseUrl, target: "_blank", rel: "noopener" }, "release notes")] : null);
  return h("div", { class: "module" },
    h("div", { class: "module-head" },
      h("span", { class: "module-name" }, m.name),
      h("span", { class: "pill " + cls }, label)),
    h("p", {}, what),
    h("p", { class: "module-ver" }, "Installed: " + (!m.installed ? "no" : (m.version || "yes (version unknown)"))),
    latestLine,
    h("p", {}, m.installed ? m.path : "Not found on this PC."),
    h("p", {}, h("a", { href: url, target: "_blank", rel: "noopener" }, "Project page")));
}

// The components card: drawn from the latest-release check when it has come back.
function renderModules() {
  const box = $("#modules-section");
  if (!box) return;
  const ms = state.latest || state.modules || { winfsp: { name: "WinFsp" }, sshfsWin: { name: "SSHFS-Win" } };
  const checked = !!(state.latest && state.latest.checked);
  const err = state.latest && state.latest.latestError;
  const running = !!installPoll;
  let label, update = false, disabled = false;
  if (!ms.ready) label = "Install what is missing";
  else if (checked && ms.updatesAvailable) { label = "Update to the latest versions"; update = true; }
  else { label = checked && !err ? "Everything is up to date" : "Everything is installed"; disabled = true; }
  const mountedCount = Object.values(state.status.devices || {}).flat().filter(r => r.state === "mounted").length;

  box.replaceChildren(...[
    h("div", { class: "module-grid" },
      moduleCard(ms.winfsp, "https://github.com/winfsp/winfsp", "Lets Windows show non-Windows file systems as drives.", checked, err),
      moduleCard(ms.sshfsWin, "https://github.com/winfsp/sshfs-win", "Connects those drives to your device over SSH.", checked, err)),
    update && mountedCount ? h("div", { class: "note warn" }, "You have " + mountedCount + " drive" + (mountedCount === 1 ? "" : "s") +
      " mounted. Unmount them before updating, or Windows may need a restart to finish.") : null,
    err ? h("div", { class: "hint" }, err) : null,
    h("div", { class: "actions", style: "margin-bottom:0" },
      h("button", { class: "btn btn-primary", id: "install-btn", disabled: disabled || running, onclick: () => startInstall(update) }, label),
      h("button", { class: "btn", onclick: async (e) => {
        busy(e.currentTarget, true);
        await Promise.all([loadModules(), loadLatest(true)]);
        runSelfTest();
        renderModules();
      } }, "Check again")),
    h("div", { class: "hint" }, "Downloads official installers from github.com/winfsp, checks them against GitHub's SHA-256 and runs them. Windows will ask for administrator permission. Updates are never installed without you pressing the button."),
  ].filter(Boolean)); // replaceChildren would print an empty slot as "null"
}

async function renderSetup(view) {
  // An install keeps running (and its log keeps filling) if you leave this page and come back.
  const ms = state.modules || { winfsp: { name: "WinFsp" }, sshfsWin: { name: "SSHFS-Win" } };
  const testList = h("ul", { class: "steps", id: "selftest" }, h("li", {}, h("span", {}), h("span", { class: "sub" }, "Running checks...")));
  const log = h("div", { class: "log", id: "install-log", hidden: !installPoll });
  const videoBtn = h("button", { class: "btn", onclick: () => {
    const p = $("#video");
    if (p.hidden) {
      p.replaceChildren(h("iframe", { src: "https://www.youtube-nocookie.com/embed/2QSpdKcXUww", title: "SSHFS setup video",
        referrerpolicy: "strict-origin-when-cross-origin", // YouTube needs it (avoids player Error 153)
        allow: "accelerometer; autoplay; clipboard-write; encrypted-media; gyroscope; picture-in-picture", allowfullscreen: true }));
      p.hidden = false;
      videoBtn.textContent = "Hide the setup video";
    } else {
      p.replaceChildren();
      p.hidden = true;
      videoBtn.textContent = "▶ Watch the setup video";
    }
  } }, "▶ Watch the setup video");

  view.replaceChildren(
    h("div", { class: "page-head" },
      h("div", { class: "page-title" },
        h("div", { class: "page-icon" }, modelIcon("setup")),
        h("div", {}, h("h1", {}, "Setup and tester"),
          h("div", { class: "sub" }, "Windows needs two free components to open SSH drives.")))),

    h("section", { class: "card" },
      h("div", { class: "card-title" }, "Components on this PC"),
      h("div", { id: "modules-section" }),
      log),

    h("section", { class: "card" },
      h("div", { class: "card-title" }, "Tester"),
      testList),

    h("section", { class: "card" },
      h("div", { class: "card-title" }, "Prepare your device"),
      h("ol", { class: "prep" },
        h("li", {}, "On the Steam Deck, Steam Machine or Steam Frame, switch to Desktop Mode and open Konsole."),
        h("li", {}, "Set a password if you never have: ", h("code", {}, "passwd")),
        h("li", {}, "Turn on SSH: ", h("code", {}, "sudo systemctl enable --now sshd")),
        h("li", {}, "Note its name (", h("code", {}, "hostname"), ") or IP address (", h("code", {}, "ip -4 addr"), ")."),
        h("li", {}, "Back here, add the device, then use Test connection.")),
      h("div", { class: "actions", style: "margin:14px 0 0" }, videoBtn),
      h("div", { class: "video-panel", id: "video", hidden: true }),
      h("div", { class: "hint" }, "Video not playing? ",
        h("a", { href: "https://www.youtube.com/watch?v=2QSpdKcXUww", target: "_blank", rel: "noopener" }, "Watch it on YouTube"), ".")),

    h("section", { class: "card" },
      h("div", { class: "card-title" }, "Command line"),
      h("pre", { class: "code-block" }, [
        'sshfs-station "Living room Deck"            mount every drive of a profile',
        'sshfs-station unmount "Living room Deck"',
        'sshfs-station status                        what is mounted',
        'sshfs-station test "Travel Deck"            check the device answers over SSH',
        'sshfs-station list | check | setup | help',
      ].join("\n")),
      h("div", { class: "hint" }, "Names ignore case and a unique start is enough. sshfs-station-cli.exe prints to the terminal and waits, so use it in scripts.")),
  );
  renderSidebar();
  renderModules();
  runSelfTest();
  loadLatest(false);
}

async function refreshSetup() {
  await Promise.all([loadModules(), loadLatest(true)]);
  if (state.view === "setup") renderSetup($("#view"));
}

async function runSelfTest() {
  try {
    const steps = await api("POST", "/api/modules/selftest");
    const list = $("#selftest");
    if (!list) return;
    list.replaceChildren(...steps.map(s => h("li", {},
      h("span", { class: "mark " + (s.ok ? "ok" : "bad") }, s.ok ? "✓" : "✗"),
      h("span", {}, s.name),
      h("span", { class: "step-detail" }, s.detail))));
  } catch (e) { toast(e.message, "bad"); }
}

async function startInstall(update) {
  const btn = $("#install-btn");
  busy(btn, true);
  const log = $("#install-log");
  log.hidden = false;
  log.textContent = "Starting...\n";
  try {
    await api("POST", "/api/modules/install", { update: !!update });
  } catch (e) {
    busy(btn, false);
    log.textContent += e.message + "\n";
    return;
  }
  clearInterval(installPoll);
  installPoll = setInterval(async () => {
    let st;
    try { st = await api("GET", "/api/modules/install"); } catch (e) { return; }
    const el = $("#install-log");
    if (el) {
      el.textContent = st.log.join("\n") + (st.error ? "\n\nProblem: " + st.error : "");
      el.scrollTop = el.scrollHeight;
    }
    if (st.done) {
      clearInterval(installPoll);
      installPoll = null;
      await loadModules();
      await loadLatest(true);
      toast(st.error ? "Install did not finish: " + st.error
        : update ? "WinFsp and SSHFS-Win are up to date." : "WinFsp and SSHFS-Win are installed.", st.error ? "bad" : "ok");
      if (state.view === "setup") {
        const keep = el ? el.textContent : "";
        renderSetup($("#view"));
        const nl = $("#install-log");
        if (nl) { nl.hidden = false; nl.textContent = keep; }
      }
    }
  }, 700);
}

// ---------------------------------------------------------------------------
// Keep-alive: the app quits when its window has been closed.
// ---------------------------------------------------------------------------
function ping() { api("POST", "/api/ping").catch(() => {}); }
window.addEventListener("pagehide", () => {
  navigator.sendBeacon("/api/bye?t=" + encodeURIComponent(TOKEN));
});

// ---------------------------------------------------------------------------
// Start
// ---------------------------------------------------------------------------
async function start() {
  $("#add-device").addEventListener("click", pickModel);
  $("#nav-setup").addEventListener("click", showSetup);
  $("#banner-setup").addEventListener("click", showSetup);
  try {
    state.info = await api("GET", "/api/info");
  } catch (e) {
    $("#view").replaceChildren(h("div", { class: "welcome" }, h("h1", {}, "SSHFS Station could not start"), h("p", {}, e.message)));
    return;
  }
  ping();
  setInterval(ping, 5000);
  $("#brand-sub").textContent = "Version " + state.info.version;
  $("#demo-banner").hidden = !state.info.demo;
  await Promise.all([loadDevices(), loadModules()]);
  await loadStatus();
  if (state.devices.length) await selectDevice(state.devices[0].id);
  else if (state.modules && !state.modules.ready) await showSetup();
  else render();
  setInterval(loadStatus, 4000);
  setInterval(loadModules, 15000);
  loadLatest(false); // quiet background check: only colours the Setup dot
}

start();
