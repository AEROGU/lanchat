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
  expired: false,
};

const timeFmt = new Intl.DateTimeFormat("es", { hour: "2-digit", minute: "2-digit" });
const dayFmt = new Intl.DateTimeFormat("es", { weekday: "long", day: "numeric", month: "long", year: "numeric" });
const encoder = new TextEncoder();

// ---------- Comunicación con el servidor ----------

class ApiError extends Error {}

async function request(method, path, body) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
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
  const div = document.createElement("div");
  div.className = `msg ${m.outgoing ? "out" : "in"} ${m.status}`;
  div.dataset.id = m.id;
  div.textContent = m.body;
  const meta = document.createElement("span");
  meta.className = "meta";
  meta.textContent = timeFmt.format(new Date(m.at));
  if (m.status === "pending") meta.title = "Pendiente: se entregará cuando el contacto se conecte";
  div.append(meta);
  return div;
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
  if (i >= 0) conv.list[i] = m;
  else conv.list.push(m);
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
  $("about").textContent = `LanChat ${s.version} · ${s.hostname}`;
  $("settings-error").hidden = true;
  $("settings").showModal();
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
