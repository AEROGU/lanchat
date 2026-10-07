// Interfaz de LanChat. Habla con el servidor local:
//   GET  /api/state, /api/messages        estado y conversaciones
//   POST /api/messages, /api/read, ...    acciones
//   GET  /api/events (Server-Sent Events)  cambios en vivo
// El texto de los usuarios siempre se inserta con textContent, nunca como HTML.

const $ = (id) => document.getElementById(id);

const state = {
  self: null,
  limits: { maxName: 64, maxMessageBytes: 65536 },
  contacts: new Map(), // id -> contacto
  current: null, // id del contacto abierto
  messages: new Map(), // id de contacto -> { list: [], complete: bool }
  progress: new Map(), // id de transferencia -> { done, total, rate }
  downloadDir: "",
  expired: false,
};

// Extensiones de programas o scripts: se pide confirmación antes de abrirlos.
const RISKY_EXT = /\.(exe|msi|msix|bat|cmd|com|scr|pif|cpl|ps1|psm1|vbs|vbe|js|jse|wsf|wsh|hta|lnk|reg|jar|dll|appx)$/i;

const timeFmt = new Intl.DateTimeFormat("es", { hour: "2-digit", minute: "2-digit" });
const dayFmt = new Intl.DateTimeFormat("es", { weekday: "long", day: "numeric", month: "long", year: "numeric" });
const encoder = new TextEncoder();

// ---------- Comunicación con el servidor ----------

class ApiError extends Error {}

async function request(method, path, body) {
  const opts = { method, headers: {} };
  if (body instanceof FormData) {
    opts.body = body; // el navegador pone el Content-Type multipart
  } else if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch(path, opts);
  } catch {
    throw new ApiError("LanChat no responde. ¿Se cerró el programa?");
  }
  if (res.status === 401 || res.status === 403) {
    expire();
    throw new ApiError("Sesión caducada.");
  }
  if (res.status === 204) return null;
  const data = await res.json().catch(() => null);
  if (!res.ok) throw new ApiError(data?.error ?? `Error ${res.status}`);
  return data;
}

const api = {
  state: () => request("GET", "/api/state"),
  history: (peer, before) =>
    request("GET", `/api/messages?${new URLSearchParams({ peer, before: before ?? "" })}`),
  send: (peer, body) => request("POST", "/api/messages", { peer, body }),
  read: (peer) => request("POST", "/api/read", { peer }),
  name: (name) => request("POST", "/api/name", { name }),
  alias: (peer, alias) => request("POST", "/api/alias", { peer, alias }),
  manualPeers: (peers) => request("POST", "/api/manual-peers", { peers }),
  presence: (focused, viewing) => request("POST", "/api/presence", { focused, viewing }),
  pickFiles: (peer) => request("POST", "/api/files/pick", { peer }),
  upload: (peer, form) => request("POST", `/api/files/upload?${new URLSearchParams({ peer })}`, form),
  transfer: (action, id) => request("POST", `/api/transfers/${action}`, { id }),
  openFile: (id, index, reveal) => request("POST", "/api/files/open", { id, index, reveal }),
  downloadDir: (dir) => request("POST", "/api/download-dir", { dir }),
  openDownloadDir: () => request("POST", "/api/download-dir/open", {}),
  system: () => request("GET", "/api/system"),
  autostart: (enabled) => request("POST", "/api/system/autostart", { enabled }),
  firewall: () => request("POST", "/api/system/firewall", {}),
};

function expire() {
  state.expired = true;
  showBanner("Esta ventana es de una sesión anterior de LanChat. Ciérrala y ábrela desde el icono de la bandeja.");
}

let bannerTimer;
function showBanner(text, ms) {
  const b = $("banner");
  b.textContent = text;
  b.hidden = false;
  clearTimeout(bannerTimer);
  if (ms) bannerTimer = setTimeout(() => (b.hidden = true), ms);
}

function hideBanner() {
  if (!state.expired) $("banner").hidden = true;
}

// ---------- Estado ----------

async function loadState() {
  const s = await api.state();
  state.self = s.self;
  state.limits = s.limits;
  state.manualPeers = s.manualPeers;
  state.downloadDir = s.downloadDir;
  state.contacts = new Map(s.contacts.map((c) => [c.id, c]));
  renderSelf();
  renderContacts();
  if (state.current) {
    renderHeader();
    state.messages.clear(); // pudieron llegar mensajes mientras no había conexión
    await loadHistory(state.current);
  }
}

function sortedContacts() {
  return [...state.contacts.values()].sort(
    (a, b) => b.online - a.online || a.displayName.localeCompare(b.displayName, "es", { sensitivity: "base" }),
  );
}

function totalUnread() {
  let n = 0;
  for (const c of state.contacts.values()) n += c.unread;
  return n;
}

// ---------- Render: barra lateral ----------

function renderSelf() {
  const s = state.self;
  $("me-name").textContent = s.name || s.hostname;
  $("me-detail").textContent = s.name ? s.hostname : "Sin nombre: haz clic en ⚙ para elegirlo";
}

function renderContacts() {
  const filter = $("search").value.trim().toLocaleLowerCase("es");
  const ul = $("contacts");
  ul.replaceChildren();
  const list = sortedContacts().filter(
    (c) => !filter || `${c.displayName} ${c.detail}`.toLocaleLowerCase("es").includes(filter),
  );
  for (const c of list) {
    const li = document.createElement("li");
    li.className = "contact" + (c.online ? "" : " offline") + (c.id === state.current ? " selected" : "");
    li.title = c.online ? "En línea" : "Desconectado";

    const dot = document.createElement("span");
    dot.className = "dot" + (c.online ? " online" : "");

    const text = document.createElement("div");
    text.className = "text";
    const name = document.createElement("span");
    name.className = "name";
    name.textContent = c.displayName;
    const detail = document.createElement("span");
    detail.className = "detail";
    detail.textContent = c.detail;
    text.append(name, detail);

    li.append(dot, text);
    if (c.unread > 0) {
      const badge = document.createElement("span");
      badge.className = "badge";
      badge.textContent = c.unread > 99 ? "99+" : c.unread;
      li.append(badge);
    }
    li.addEventListener("click", () => openChat(c.id));
    ul.append(li);
  }
  $("no-contacts").hidden = state.contacts.size > 0;

  const total = totalUnread();
  document.title = total > 0 ? `(${total}) LanChat` : "LanChat";
}

// ---------- Render: conversación ----------

function renderHeader() {
  const c = state.contacts.get(state.current);
  if (!c) return;
  $("peer-name").textContent = c.displayName;
  let detail = c.detail;
  if (c.appVersion && c.appVersion !== state.self.version) {
    detail += ` · versión ${c.appVersion} (la tuya es ${state.self.version})`;
  }
  $("peer-detail").textContent = detail;
  $("peer-dot").className = "dot" + (c.online ? " online" : "");
  const note = $("offline-note");
  note.hidden = c.online;
  note.textContent = `${c.displayName} está desconectado. Los mensajes que envíes se entregarán cuando se conecte.`;
}

function dayLabel(ts) {
  const d = new Date(ts);
  const today = new Date();
  const yesterday = new Date();
  yesterday.setDate(today.getDate() - 1);
  if (d.toDateString() === today.toDateString()) return "Hoy";
  if (d.toDateString() === yesterday.toDateString()) return "Ayer";
  return dayFmt.format(d);
}

function messageElement(m) {
  if (m.kind === "files" && m.transfer) return transferCard(m);
  const div = document.createElement("div");
  div.className = `msg ${m.outgoing ? "out" : "in"} ${m.status}`;
  div.dataset.id = m.id;
  div.textContent = m.body;
  div.append(metaElement(m));
  return div;
}

function metaElement(m) {
  const meta = document.createElement("span");
  meta.className = "meta";
  meta.textContent = timeFmt.format(new Date(m.at));
  if (m.status === "pending") meta.title = "Pendiente: se entregará cuando el contacto se conecte";
  return meta;
}

// ---------- Render: archivos ----------

const expiryFmt = new Intl.DateTimeFormat("es", { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });

function fmtSize(n) {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return i === 0 ? `${n} B` : `${n.toFixed(1)} ${units[i]}`;
}

function textButton(label, onClick, primary = false) {
  const b = document.createElement("button");
  b.type = "button";
  b.className = primary ? "primary small" : "text-btn";
  b.textContent = label;
  b.addEventListener("click", onClick);
  return b;
}

function transferStatus(m, t) {
  const who = state.contacts.get(m.peerId)?.displayName ?? "El contacto";
  const until = `Disponible hasta el ${expiryFmt.format(new Date(t.expiresAt))}.`;
  const one = t.files.length === 1;
  if (t.outgoing) {
    switch (t.state) {
      case "offered":
        return m.status === "pending"
          ? `Se le entregará la oferta cuando ${who} se conecte. ${until}`
          : `Esperando que ${who} ${one ? "lo acepte" : "los acepte"}. ${until}`;
      case "downloading": return `${who} está descargando…`;
      case "completed": return `${who} ${one ? "recibió el archivo" : "recibió los archivos"} ✓`;
      case "rejected": return `${who} ${one ? "rechazó el archivo" : "rechazó los archivos"}.`;
      case "canceled": return "Envío cancelado.";
      case "expired": return "La oferta caducó sin descargarse.";
    }
  } else {
    switch (t.state) {
      case "offered": return `${who} quiere enviarte ${one ? "este archivo" : "estos archivos"}. ${until}`;
      case "downloading": return "Descargando…";
      case "completed": return `Guardado en ${t.dir}`;
      case "rejected": return `Rechazaste ${one ? "el archivo" : "los archivos"}.`;
      case "canceled": return t.error || "Envío cancelado.";
      case "expired": return t.error || "La oferta caducó; pide que te lo vuelvan a enviar.";
      case "failed": return `No se pudo descargar: ${t.error}`;
    }
  }
  return t.state;
}

function transferCard(m) {
  const t = m.transfer;
  const div = document.createElement("div");
  div.className = `msg ${m.outgoing ? "out" : "in"} files ${t.state} ${m.status}`;
  div.dataset.id = m.id;

  const title = document.createElement("div");
  title.className = "files-title";
  title.textContent = `📎 ${t.files.length === 1 ? "1 archivo" : `${t.files.length} archivos`} · ${fmtSize(t.total)}`;
  div.append(title);

  const ul = document.createElement("ul");
  ul.className = "file-list";
  for (const f of t.files) {
    const li = document.createElement("li");
    const name = document.createElement("span");
    name.className = "file-name";
    name.textContent = f.savedName || f.name;
    name.title = f.savedName && f.savedName !== f.name ? `Original: ${f.name}` : f.name;
    const size = document.createElement("span");
    size.className = "file-size";
    size.textContent = fmtSize(f.size) + (f.done && t.state !== "completed" ? " ✓" : "");
    li.append(name, size);
    if (!t.outgoing && f.done) {
      li.append(textButton("Abrir", () => openReceived(t, f, false)),
        textButton("Mostrar en carpeta", () => openReceived(t, f, true)));
    }
    ul.append(li);
  }
  div.append(ul);

  if (t.state === "downloading") {
    const bar = document.createElement("div");
    bar.className = "progress";
    bar.append(document.createElement("div"));
    const info = document.createElement("div");
    info.className = "progress-info";
    div.append(bar, info);
    const done = t.files.filter((f) => f.done).reduce((n, f) => n + f.size, 0);
    paintProgress(div, state.progress.get(t.id) ?? { done, total: t.total, rate: 0 });
  }

  const status = document.createElement("div");
  status.className = "status";
  status.textContent = transferStatus(m, t);
  div.append(status);

  const actions = document.createElement("div");
  actions.className = "actions-row";
  const act = (label, action, primary, confirmText) =>
    textButton(label, () => transferAction(action, t.id, confirmText), primary);
  if (!t.outgoing && t.state === "offered") {
    actions.append(act("Aceptar", "accept", true), act("Rechazar", "reject"));
  } else if (!t.outgoing && t.state === "failed") {
    actions.append(act("Reintentar", "accept", true), act("Cancelar", "cancel", false, "¿Cancelar la descarga?"));
  } else if (t.state === "downloading" || (t.outgoing && t.state === "offered")) {
    actions.append(act(t.outgoing ? "Cancelar envío" : "Cancelar", "cancel", false,
      t.outgoing ? "¿Cancelar el envío?" : "¿Cancelar la descarga?"));
  }
  if (actions.childElementCount) div.append(actions);

  div.append(metaElement(m));
  return div;
}

function paintProgress(card, p) {
  const bar = card.querySelector(".progress > div");
  const info = card.querySelector(".progress-info");
  if (!bar || !info) return;
  const pct = p.total > 0 ? Math.min(100, (p.done / p.total) * 100) : 0;
  bar.style.width = `${pct}%`;
  info.textContent = `${pct.toFixed(0)}% · ${fmtSize(p.done)} de ${fmtSize(p.total)}` +
    (p.rate > 0 ? ` · ${fmtSize(Math.round(p.rate))}/s` : "");
}

function cardElement(id) {
  return $("messages").querySelector(`.msg[data-id="${CSS.escape(id)}"]`);
}

async function transferAction(action, id, confirmText) {
  if (confirmText && !confirm(confirmText)) return;
  try {
    await api.transfer(action, id);
  } catch (e) {
    showBanner(e.message, 5000);
  }
}

async function openReceived(t, f, reveal) {
  const name = f.savedName || f.name;
  if (!reveal && RISKY_EXT.test(name)) {
    const who = state.contacts.get(t.peerId)?.displayName ?? "el remitente";
    if (!confirm(`"${name}" es un programa o script. Ábrelo solo si confías en ${who} y esperabas este archivo. ¿Abrirlo?`)) return;
  }
  try {
    await api.openFile(t.id, f.index, reveal);
  } catch (e) {
    showBanner(e.message, 5000);
  }
}

// Actualiza la oferta guardada en la conversación y su tarjeta en pantalla.
function updateTransfer(t) {
  if (t.state !== "downloading") state.progress.delete(t.id);
  const conv = state.messages.get(t.peerId);
  const m = conv?.list.find((x) => x.id === t.id);
  if (!m) return;
  m.transfer = t;
  const old = t.peerId === state.current ? cardElement(t.id) : null;
  if (old) old.replaceWith(messageElement(m));
}

async function pickAndSend() {
  const btn = $("attach-btn");
  btn.disabled = true;
  try {
    const m = await api.pickFiles(state.current);
    if (m) addMessage(m);
  } catch (e) {
    showBanner(e.message, 5000);
  } finally {
    btn.disabled = false;
  }
}

async function uploadDropped(dt) {
  if (!state.current) return showBanner("Elige primero a quién enviarle los archivos.", 4000);
  const entries = [...dt.items].map((i) => i.webkitGetAsEntry?.()).filter(Boolean);
  if (entries.some((e) => e.isDirectory)) {
    return showBanner("Las carpetas todavía no se pueden enviar; arrastra los archivos.", 5000);
  }
  const files = [...dt.files];
  if (files.length === 0) return;
  const form = new FormData();
  for (const f of files) form.append("files", f, f.name);
  showBanner(`Preparando ${files.length === 1 ? files[0].name : `${files.length} archivos`}…`);
  try {
    const m = await api.upload(state.current, form);
    hideBanner();
    addMessage(m);
  } catch (e) {
    showBanner(e.message, 5000);
  }
}

function renderMessages({ keepScroll = false, toBottom = false } = {}) {
  const box = $("messages");
  const conv = state.messages.get(state.current);
  const prevHeight = box.scrollHeight;
  const prevTop = box.scrollTop;
  const nearBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 80;

  box.replaceChildren();
  let lastDay = "";
  for (const m of conv?.list ?? []) {
    const day = new Date(m.at).toDateString();
    if (day !== lastDay) {
      const sep = document.createElement("div");
      sep.className = "day";
      sep.textContent = dayLabel(m.at);
      box.append(sep);
      lastDay = day;
    }
    box.append(messageElement(m));
  }

  if (keepScroll) box.scrollTop = box.scrollHeight - prevHeight + prevTop;
  else if (toBottom || nearBottom) box.scrollTop = box.scrollHeight;
}

// ---------- Acciones ----------

async function openChat(id) {
  state.current = id;
  $("app").classList.add("chatting");
  $("empty").hidden = true;
  $("chat-header").hidden = false;
  $("composer").hidden = false;
  $("send-error").hidden = true;
  renderHeader();
  renderContacts();
  renderMessages({ toBottom: true });
  reportPresence();
  if (!state.messages.has(id)) await loadHistory(id);
  markReadIfVisible();
  $("input").focus();
}

function closeChat() {
  state.current = null;
  $("app").classList.remove("chatting");
  reportPresence();
}

async function loadHistory(peer, before) {
  let page;
  try {
    page = await api.history(peer, before);
  } catch (e) {
    showBanner(e.message, 4000);
    return;
  }
  const conv = state.messages.get(peer) ?? { list: [], complete: false };
  const known = new Set(conv.list.map((m) => m.id));
  conv.list = [...page.filter((m) => !known.has(m.id)), ...conv.list];
  conv.complete = page.length === 0;
  state.messages.set(peer, conv);
  if (peer === state.current) renderMessages(before ? { keepScroll: true } : { toBottom: true });
}

function addMessage(m) {
  const conv = state.messages.get(m.peerId);
  if (!conv) return; // la conversación se cargará completa al abrirla
  const i = conv.list.findIndex((x) => x.id === m.id);
  if (i >= 0) {
    m.transfer ??= conv.list[i].transfer; // conservar la oferta si el evento no la trae
    conv.list[i] = m;
  } else {
    conv.list.push(m);
  }
  if (m.peerId === state.current) renderMessages({ toBottom: m.outgoing });
}

async function sendMessage() {
  const input = $("input");
  const body = input.value;
  const err = $("send-error");
  err.hidden = true;
  if (!body.trim() || !state.current) return;
  if (encoder.encode(body).length > state.limits.maxMessageBytes) {
    err.textContent = `El mensaje es demasiado largo (máximo ${state.limits.maxMessageBytes / 1024} KB).`;
    err.hidden = false;
    return;
  }
  try {
    const m = await api.send(state.current, body);
    input.value = "";
    autoGrow();
    addMessage(m);
  } catch (e) {
    err.textContent = e.message;
    err.hidden = false;
  }
}

function windowFocused() {
  return document.visibilityState === "visible" && document.hasFocus();
}

function reportPresence() {
  api.presence(windowFocused(), state.current ?? "").catch(() => {});
}

function markReadIfVisible() {
  const c = state.contacts.get(state.current);
  if (c && c.unread > 0 && windowFocused()) {
    c.unread = 0; // el servidor confirmará con un evento "contact"
    renderContacts();
    api.read(c.id).catch(() => {});
  }
}

function autoGrow() {
  const t = $("input");
  t.style.height = "auto";
  t.style.height = `${t.scrollHeight}px`;
}

// ---------- Diálogos ----------

function openSettings() {
  const s = state.self;
  $("name-input").value = s.name;
  $("name-input").placeholder = s.hostname;
  $("name-input").maxLength = state.limits.maxName;
  $("name-help").textContent = `Así te verán los demás. Vacío = se usa el nombre del equipo (${s.hostname}).`;
  $("peers-input").value = (state.manualPeers ?? []).join("\n");
  $("download-input").value = state.downloadDir;
  $("about").textContent = `LanChat ${s.version} · ${s.hostname}`;
  $("settings-error").hidden = true;
  $("settings").showModal();
  api.system().then(renderSystem).catch(() => {});
}

function renderSystem(sys) {
  $("system-section").hidden = !sys.supported;
  $("autostart-input").checked = sys.autostart;
  $("firewall-status").textContent = sys.firewall ? "Permitido ✓" : "Sin configurar";
  $("firewall-btn").hidden = sys.firewall;
}

function settingsError(e) {
  $("settings-error").textContent = e.message;
  $("settings-error").hidden = false;
}

async function toggleAutostart() {
  try {
    renderSystem(await api.autostart($("autostart-input").checked));
  } catch (e) {
    settingsError(e);
  }
}

async function allowFirewall() {
  const btn = $("firewall-btn");
  btn.disabled = true;
  $("firewall-status").textContent = "Esperando el permiso de administrador…";
  try {
    renderSystem(await api.firewall());
  } catch (e) {
    settingsError(e);
    api.system().then(renderSystem).catch(() => {});
  } finally {
    btn.disabled = false;
  }
}

async function saveSettings(ev) {
  if (ev.submitter?.value !== "save") return;
  ev.preventDefault();
  const err = $("settings-error");
  err.hidden = true;
  try {
    state.self = await api.name($("name-input").value);
    const peers = $("peers-input").value.split("\n");
    state.manualPeers = (await api.manualPeers(peers)).manualPeers;
    const dir = $("download-input").value.trim();
    if (dir !== state.downloadDir) state.downloadDir = (await api.downloadDir(dir)).downloadDir;
    renderSelf();
    $("settings").close();
  } catch (e) {
    err.textContent = e.message;
    err.hidden = false;
  }
}

function openAlias() {
  const c = state.contacts.get(state.current);
  if (!c) return;
  $("alias-input").value = c.alias;
  $("alias-input").placeholder = c.name || c.hostname || c.ip;
  $("alias-input").maxLength = state.limits.maxName;
  $("alias-error").hidden = true;
  $("alias-dialog").showModal();
}

async function saveAlias(ev) {
  if (ev.submitter?.value !== "save") return;
  ev.preventDefault();
  try {
    await api.alias(state.current, $("alias-input").value);
    $("alias-dialog").close();
  } catch (e) {
    $("alias-error").textContent = e.message;
    $("alias-error").hidden = false;
  }
}

// ---------- Eventos en vivo ----------

function connectEvents() {
  const es = new EventSource("/api/events");
  let wasDown = false;

  es.addEventListener("open", async () => {
    hideBanner();
    if (wasDown) {
      wasDown = false;
      await loadState().catch(() => {});
    }
    reportPresence();
  });

  es.addEventListener("error", async () => {
    if (state.expired) return es.close();
    wasDown = true;
    showBanner("Reconectando con LanChat…");
    // Si el servidor responde pero rechaza el token, la sesión caducó.
    try {
      const res = await fetch("/api/state");
      if (res.status === 401 || res.status === 403) {
        expire();
        es.close();
      }
    } catch {
      // Sin respuesta: EventSource seguirá reintentando.
    }
  });

  es.addEventListener("contact", (e) => {
    const c = JSON.parse(e.data);
    state.contacts.set(c.id, c);
    renderContacts();
    if (c.id === state.current) {
      renderHeader();
      if (c.unread > 0) markReadIfVisible();
    }
  });

  es.addEventListener("message", (e) => {
    const m = JSON.parse(e.data);
    addMessage(m);
    if (!m.outgoing && m.peerId === state.current) markReadIfVisible();
  });

  es.addEventListener("transfer", (e) => updateTransfer(JSON.parse(e.data)));

  es.addEventListener("progress", (e) => {
    const p = JSON.parse(e.data);
    state.progress.set(p.id, p);
    const card = cardElement(p.id);
    if (card) paintProgress(card, p);
  });

  es.addEventListener("focus", () => window.focus());
}

// ---------- Inicio ----------

function bind() {
  $("search").addEventListener("input", renderContacts);
  $("settings-btn").addEventListener("click", openSettings);
  $("settings-form").addEventListener("submit", saveSettings);
  $("alias-btn").addEventListener("click", openAlias);
  $("alias-form").addEventListener("submit", saveAlias);
  $("back-btn").addEventListener("click", closeChat);

  $("composer").addEventListener("submit", (e) => {
    e.preventDefault();
    sendMessage();
  });
  $("input").addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      sendMessage();
    }
  });
  $("input").addEventListener("input", autoGrow);
  $("attach-btn").addEventListener("click", pickAndSend);
  $("autostart-input").addEventListener("change", toggleAutostart);
  $("firewall-btn").addEventListener("click", allowFirewall);
  $("open-downloads").addEventListener("click", () =>
    api.openDownloadDir().catch((e) => showBanner(e.message, 5000)));

  // Arrastrar y soltar archivos sobre la conversación.
  const chat = $("chat");
  let dragDepth = 0;
  const hasFiles = (e) => e.dataTransfer?.types.includes("Files");
  chat.addEventListener("dragenter", (e) => {
    if (!hasFiles(e) || !state.current) return;
    dragDepth++;
    $("drop-overlay").textContent = `Suelta para enviar a ${state.contacts.get(state.current)?.displayName ?? ""}`;
    $("drop-overlay").hidden = false;
  });
  chat.addEventListener("dragleave", () => {
    if (--dragDepth <= 0) {
      dragDepth = 0;
      $("drop-overlay").hidden = true;
    }
  });
  chat.addEventListener("drop", (e) => {
    dragDepth = 0;
    $("drop-overlay").hidden = true;
    if (hasFiles(e)) uploadDropped(e.dataTransfer);
  });
  // Evita que soltar un archivo fuera de la zona lo abra en la ventana.
  for (const ev of ["dragover", "drop"]) window.addEventListener(ev, (e) => e.preventDefault());

  $("messages").addEventListener("scroll", () => {
    const conv = state.messages.get(state.current);
    if ($("messages").scrollTop < 40 && conv && !conv.complete && conv.list.length > 0 && !conv.loading) {
      conv.loading = true;
      loadHistory(state.current, conv.list[0].id).finally(() => (conv.loading = false));
    }
  });

  for (const ev of ["focus", "blur"]) window.addEventListener(ev, () => {
    reportPresence();
    markReadIfVisible();
  });
  document.addEventListener("visibilitychange", () => {
    reportPresence();
    markReadIfVisible();
  });
}

bind();
loadState()
  .then(connectEvents)
  .catch((e) => showBanner(e.message));
