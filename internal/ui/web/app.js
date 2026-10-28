// Interfaz de LanChat. Habla con el servidor local:
//   GET  /api/state, /api/messages        estado y conversaciones
//   POST /api/messages, /api/read, ...    acciones
//   /api/rooms...                         salas de chat grupales
//   GET  /api/events (Server-Sent Events)  cambios en vivo
// El texto de los usuarios siempre se inserta con textContent, nunca como HTML.

import { EMOJI_GROUPS } from "/emoji.js";

const $ = (id) => document.getElementById(id);

const state = {
  self: null,
  limits: { maxName: 64, maxMessageBytes: 65536, deleteWindowMs: 900000 },
  contacts: new Map(), // id -> contacto
  rooms: new Map(), // id -> sala
  // current es la conversación abierta: el id del contacto o ROOM + id de la sala.
  current: null,
  messages: new Map(), // conversación -> { list: [], complete: bool }
  progress: new Map(), // id de transferencia -> { done, total, rate }
  downloadDir: "",
  expired: false,
  // editing: id del mensaje propio que se está editando (null = ninguno);
  // editDraft: lo que había escrito antes, para devolverlo al terminar.
  editing: null,
  editDraft: "",
  // selecting: modo de selección de varios mensajes (selected: sus ids).
  selecting: false,
  selected: new Set(),
};

// Extensiones de programas o scripts: se pide confirmación antes de abrirlos.
const RISKY_EXT = /\.(exe|msi|msix|bat|cmd|com|scr|pif|cpl|ps1|psm1|vbs|vbe|js|jse|wsf|wsh|hta|lnk|reg|jar|dll|appx)$/i;

const timeFmt = new Intl.DateTimeFormat("es", { hour: "2-digit", minute: "2-digit" });
const dayFmt = new Intl.DateTimeFormat("es", { weekday: "long", day: "numeric", month: "long", year: "numeric" });
const encoder = new TextEncoder();

// ROOM antecede al id de una sala en state.current; coincide con roomViewPrefix
// del servidor, que usa la misma clave para no notificar la sala que se ve.
const ROOM = "room:";
const roomId = (key) => (key?.startsWith(ROOM) ? key.slice(ROOM.length) : null);
const currentRoom = () => state.rooms.get(roomId(state.current));
const convKey = (m) => (m.roomId ? ROOM + m.roomId : m.peerId);

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
  status: (status, text, autoAway) => request("POST", "/api/status", { status, text, autoAway }),
  sendMany: (peers, body) => request("POST", "/api/messages/many", { peers, body }),
  deleteMessage: (id) => request("POST", "/api/messages/delete", { id }),
  react: (id, emoji) => request("POST", "/api/messages/react", { id, emoji }),
  editMessage: (id, body) => request("POST", "/api/messages/edit", { id, body }),
  forward: (ids, peers, rooms) => request("POST", "/api/messages/forward", { ids, peers, rooms }),
  readReceipts: (enabled) => request("POST", "/api/read-receipts", { enabled }),
  previews: (enabled) => request("POST", "/api/previews", { enabled }),
  thumbsUsage: () => request("GET", "/api/previews/stored"),
  clearThumbs: () => request("POST", "/api/previews/clear", {}),
  group: (peer, group) => request("POST", "/api/group", { peer, group }),
  alias: (peer, alias) => request("POST", "/api/alias", { peer, alias }),
  manualPeers: (peers) => request("POST", "/api/manual-peers", { peers }),
  presence: (focused, viewing) => request("POST", "/api/presence", { focused, viewing }),
  pickFiles: (peer) => request("POST", "/api/files/pick", { peer }),
  pickFolder: (peer) => request("POST", "/api/files/pick-folder", { peer }),
  openDir: (id, dir) => request("POST", "/api/files/open-dir", { id, dir }),
  upload: (peer, form) => request("POST", `/api/files/upload?${new URLSearchParams({ peer })}`, form),
  transfer: (action, id) => request("POST", `/api/transfers/${action}`, { id }),
  openFile: (id, index, reveal) => request("POST", "/api/files/open", { id, index, reveal }),
  downloadDir: (dir) => request("POST", "/api/download-dir", { dir }),
  openDownloadDir: () => request("POST", "/api/download-dir/open", {}),
  system: () => request("GET", "/api/system"),
  autostart: (enabled) => request("POST", "/api/system/autostart", { enabled }),
  firewall: () => request("POST", "/api/system/firewall", {}),
  uninstall: () => request("POST", "/api/system/uninstall", {}),
  createRoom: (name, members) => request("POST", "/api/rooms", { name, members }),
  roomHistory: (room, before) =>
    request("GET", `/api/rooms/messages?${new URLSearchParams({ room, before: before ?? "" })}`),
  roomSend: (room, body) => request("POST", "/api/rooms/messages", { room, body }),
  // action: members, rename, leave, kick, admin, destroy, delete o read.
  room: (action, room, extra) => request("POST", `/api/rooms/${action}`, { room, ...extra }),
  deleteConversation: (peer) => request("POST", "/api/conversation/delete", { peer }),
  removeContact: (peer, into) => request("POST", "/api/contact/remove", { peer, into }),
  wipe: (confirm) => request("POST", "/api/data/wipe", { confirm }),
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
  // Como popover queda en la capa superior, encima de un diálogo abierto
  // (p. ej. "Descargar mis datos" en Ajustes). Se reabre para quedar arriba
  // de un diálogo abierto después.
  if (b.showPopover) {
    if (b.matches(":popover-open")) b.hidePopover();
    b.showPopover();
  }
  clearTimeout(bannerTimer);
  if (ms) bannerTimer = setTimeout(closeBanner, ms);
}

function closeBanner() {
  const b = $("banner");
  b.hidden = true;
  if (b.hidePopover && b.matches(":popover-open")) b.hidePopover();
}

function hideBanner() {
  if (!state.expired) closeBanner();
}

// ---------- Estado ----------

async function loadState() {
  const s = await api.state();
  state.self = s.self;
  state.limits = s.limits;
  state.manualPeers = s.manualPeers;
  state.downloadDir = s.downloadDir;
  state.mobile = s.mobile;
  document.body.classList.toggle("mobile", s.mobile);
  if (s.mobile) {
    $("input").placeholder = "Escribe un mensaje";
    $("autoaway-text").textContent = "Ponerme Ausente tras 10 minutos con la pantalla apagada";
    // Que abrir Ajustes no despliegue el teclado: el foco va al título y no
    // al campo del nombre.
    const title = $("settings").querySelector("h2");
    title.tabIndex = -1;
    title.autofocus = true;
  }
  state.contacts = new Map(s.contacts.map((c) => [c.id, c]));
  state.rooms = new Map(s.rooms.map((r) => [r.id, r]));
  renderSelf();
  renderContacts();
  if (state.current) {
    renderHeader();
    state.messages.clear(); // pudieron llegar mensajes mientras no había conexión
    await loadHistory(state.current);
  }
  state.loaded = true;
  if (pendingOpen) {
    const id = pendingOpen;
    pendingOpen = null;
    window.lanchatOpen(id);
  }
  if (pendingShare) {
    const share = pendingShare;
    pendingShare = null;
    openShare(share);
  }
}

// lanchatOpen lo llama la app de Android al tocar el aviso de un mensaje: abre
// esa conversación (id del contacto o ROOM + id de la sala), en cuanto la
// lista esté cargada.
let pendingOpen = null;
window.lanchatOpen = (id) => {
  if (!state.loaded) {
    pendingOpen = id;
    return;
  }
  if (state.contacts.has(id) || state.rooms.has(roomId(id))) openChat(id);
};

// sortedContacts: los de la lista. Los quitados siguen en state.contacts solo
// para nombrarlos en las salas.
function sortedContacts() {
  return [...state.contacts.values()].filter((c) => !c.removed).sort(
    (a, b) => b.online - a.online || a.displayName.localeCompare(b.displayName, "es", { sensitivity: "base" }),
  );
}

function totalUnread() {
  let n = 0;
  for (const c of state.contacts.values()) n += c.unread;
  for (const r of state.rooms.values()) n += r.unread;
  return n;
}

// ---------- Render: barra lateral ----------

const STATUS_LABELS = { available: "Disponible", away: "Ausente", busy: "Ocupado" };

// statusLabel describe el estado de un contacto en línea ("" si no lo está).
function statusLabel(status, text) {
  if (!status) return "";
  return STATUS_LABELS[status] + (text ? ` · ${text}` : "");
}

function dotClass(online, status) {
  return "dot" + (online ? ` online ${status || "available"}` : "");
}

function renderSelf() {
  const s = state.self;
  $("me-name").textContent = s.name || s.hostname;
  $("me-detail").textContent = s.name ? s.hostname : "Sin nombre: haz clic en ⚙ para elegirlo";
  // Inactivo y Disponible: los demás te ven Ausente.
  const shown = s.status === "available" && s.idle ? "away" : s.status;
  $("status-dot").className = dotClass(true, shown);
  $("status-label").textContent = statusLabel(shown, s.statusText) + (shown !== s.status ? " (inactivo)" : "");
}

function openStatus() {
  const s = state.self;
  for (const r of document.querySelectorAll("#status-form input[name=status]")) r.checked = r.value === s.status;
  $("status-text").value = s.statusText;
  $("status-text").maxLength = state.limits.maxStatusText ?? 80;
  $("autoaway-input").checked = s.autoAway;
  $("status-error").hidden = true;
  $("status-dialog").showModal();
}

async function saveStatus(ev) {
  if (ev.submitter?.value !== "save") return;
  ev.preventDefault();
  try {
    state.self = await api.status(
      document.querySelector("#status-form input[name=status]:checked")?.value ?? "available",
      $("status-text").value,
      $("autoaway-input").checked,
    );
    renderSelf();
    $("status-dialog").close();
  } catch (e) {
    $("status-error").textContent = e.message;
    $("status-error").hidden = false;
  }
}

// ---------- Grupos ----------

const COLLAPSED_KEY = "lanchat.collapsedGroups";
const HIDE_OFFLINE_KEY = "lanchat.hideOffline";

// Ocultar desconectados se recuerda solo en esta ventana (localStorage).
function hideOffline() {
  try {
    return localStorage.getItem(HIDE_OFFLINE_KEY) === "1";
  } catch {
    return false;
  }
}

function setHideOffline(hide) {
  try {
    localStorage.setItem(HIDE_OFFLINE_KEY, hide ? "1" : "0");
  } catch {
    // sin almacenamiento: vale hasta que se cierre la ventana
  }
  renderContacts();
}

// visibleContact: al ocultar desconectados se siguen viendo los que tienen
// mensajes sin leer y la conversación abierta; al buscar, todos.
function visibleContact(c, filter) {
  return c.online || filter || !hideOffline() || c.unread > 0 || c.id === state.current;
}
const NO_GROUP = "Sin grupo";
const ROOMS_GROUP = "Salas";
const CONTACTS_GROUP = "Contactos";

// Los grupos plegados se recuerdan solo en esta ventana (localStorage).
function collapsedGroups() {
  try {
    return new Set(JSON.parse(localStorage.getItem(COLLAPSED_KEY) ?? "[]"));
  } catch {
    return new Set();
  }
}

function toggleGroup(name) {
  const set = collapsedGroups();
  if (!set.delete(name)) set.add(name);
  try {
    localStorage.setItem(COLLAPSED_KEY, JSON.stringify([...set]));
  } catch {
    // sin almacenamiento: el grupo solo cambia hasta que se redibuje
  }
  renderContacts();
}

function groupNames() {
  const names = new Set(sortedContacts().map((c) => c.group).filter(Boolean));
  return [...names].sort((a, b) => a.localeCompare(b, "es", { sensitivity: "base" }));
}

// groupHeader muestra "▾ Nombre (en línea/total)"; las salas, solo el total.
function groupHeader(name, members, collapsed, foldable = true) {
  const li = document.createElement("li");
  li.className = "group-header";
  const count = name === ROOMS_GROUP
    ? members.length
    : `${members.filter((c) => c.online).length}/${members.length}`;
  li.textContent = `${foldable ? (collapsed ? "▸ " : "▾ ") : ""}${name} (${count})`;
  if (foldable) {
    li.title = collapsed ? "Mostrar" : "Ocultar";
    li.addEventListener("click", () => toggleGroup(name));
  }
  return li;
}

function renderContacts() {
  const filter = $("search").value.trim().toLocaleLowerCase("es");
  const ul = $("contacts");
  ul.replaceChildren();
  const matching = sortedContacts().filter(
    (c) => !filter || `${c.displayName} ${c.detail} ${c.group}`.toLocaleLowerCase("es").includes(filter),
  );
  const list = matching.filter((c) => visibleContact(c, filter));
  const hidden = matching.length - list.length;
  const rooms = sortedRooms().filter((r) => !filter || r.name.toLocaleLowerCase("es").includes(filter));
  const collapsed = filter ? new Set() : collapsedGroups(); // al buscar se muestra todo
  if (rooms.length > 0) {
    ul.append(groupHeader(ROOMS_GROUP, rooms, collapsed.has(ROOMS_GROUP)));
    if (!collapsed.has(ROOMS_GROUP)) for (const r of rooms) ul.append(roomItem(r));
  }
  const names = groupNames();
  if (names.length === 0) {
    if (rooms.length > 0 && list.length > 0) ul.append(groupHeader(CONTACTS_GROUP, matching, false, false));
    for (const c of list) ul.append(contactItem(c));
  } else {
    for (const name of [...names, NO_GROUP]) {
      // El encabezado cuenta a todos (en línea/total); se dibujan los visibles.
      const all = matching.filter((c) => (c.group || NO_GROUP) === name);
      const members = list.filter((c) => (c.group || NO_GROUP) === name);
      if (members.length === 0) continue;
      ul.append(groupHeader(name, all, collapsed.has(name)));
      if (!collapsed.has(name)) for (const c of members) ul.append(contactItem(c));
    }
  }
  if (hidden > 0) ul.append(hiddenHint(hidden));
  $("no-contacts").hidden = sortedContacts().length > 0;
  const btn = $("offline-btn");
  btn.setAttribute("aria-pressed", hideOffline());
  btn.title = hideOffline() ? "Mostrar los contactos desconectados" : "Ocultar los contactos desconectados";
  btn.setAttribute("aria-label", btn.title);

  const total = totalUnread();
  document.title = total > 0 ? `(${total}) LanChat` : "LanChat";
}

// hiddenHint avisa al final de la lista cuántos desconectados no se ven.
function hiddenHint(n) {
  const li = document.createElement("li");
  li.className = "hidden-hint";
  li.append(`${n} ${n === 1 ? "desconectado oculto" : "desconectados ocultos"} · `,
    Object.assign(document.createElement("button"), {
      type: "button",
      className: "link-btn",
      textContent: "Mostrar",
      onclick: () => setHideOffline(false),
    }));
  return li;
}

// versionLabel acorta la versión de LanChat para la lista: "v0.18.0", o
// "v0.18.0-dev" si es un compilado de pruebas ("0.18.0-3-gabc1234-dirty").
function versionLabel(v) {
  const m = /^v?(\d+\.\d+\.\d+(?:-rc\.\d+)?)(-.+)?$/.exec(v);
  if (!m) return v;
  return `v${m[1]}${m[2] ? "-dev" : ""}`;
}

// incompatibleText explica por qué no se puede escribir a un contacto con
// otra versión del protocolo ("" si es compatible).
function incompatibleText(c) {
  const url = `${state.self.repository}/releases`;
  const theirs = c.appVersion ? ` (${c.appVersion})` : "";
  if (c.incompatible === "older") {
    return `${c.displayName} usa una versión anterior de LanChat${theirs} que ya no es compatible con la tuya: no puedes enviarle ni recibir sus mensajes hasta que actualice. LanChat le envía un aviso automático para que instale la última desde ${url}`;
  }
  if (c.incompatible === "newer") {
    return `${c.displayName} usa una versión más nueva de LanChat${theirs} que ya no es compatible con la tuya: para volver a escribirse, actualiza desde ${url}`;
  }
  return "";
}

// incompatibleBadge es la marca roja "incompatible" de la lista.
function incompatibleBadge() {
  const span = document.createElement("span");
  span.className = "incompatible";
  span.textContent = "incompatible";
  return span;
}

function contactItem(c) {
  const li = document.createElement("li");
  li.className = "contact" + (c.online ? "" : " offline") + (c.id === state.current ? " selected" : "");
  li.title = c.online ? statusLabel(c.status, c.statusText) : "Desconectado";
  if (c.appVersion) li.title += `\nLanChat ${c.appVersion}`;

  const dot = document.createElement("span");
  dot.className = dotClass(c.online, c.status);

  const text = document.createElement("div");
  text.className = "text";
  const name = document.createElement("span");
  name.className = "name";
  name.textContent = c.displayName;
  if (c.identityChanged) {
    name.textContent = `⚠ ${c.displayName}`;
    li.title = "Su identidad cambió: revisa antes de enviarle algo";
  }
  const detail = document.createElement("span");
  detail.className = "detail";
  detail.textContent = c.detail;
  if (c.appVersion) detail.append(` · ${versionLabel(c.appVersion)}`);
  text.append(name, detail);
  if (c.incompatible) {
    // En su propia línea para que no la corte el ancho de la lista.
    const line = document.createElement("span");
    line.className = "status-line";
    line.append(incompatibleBadge(), c.incompatible === "older" ? " · debe actualizar" : " · actualiza tu LanChat");
    text.append(line);
    li.title = incompatibleText(c);
  } else if (c.online && c.statusText) {
    const st = document.createElement("span");
    st.className = "status-line";
    st.textContent = c.statusText;
    text.append(st);
  }

  li.append(dot, text);
  if (c.unread > 0) {
    const badge = document.createElement("span");
    badge.className = "badge";
    badge.textContent = c.unread > 99 ? "99+" : c.unread;
    li.append(badge);
  }
  li.addEventListener("click", () => openChat(c.id));
  return li;
}

function sortedRooms() {
  return [...state.rooms.values()].sort(
    (a, b) => a.left - b.left || a.name.localeCompare(b.name, "es", { sensitivity: "base" }),
  );
}

// memberName es el nombre de un miembro de sala tal como lo ve este usuario.
function memberName(id) {
  if (id === state.self.id) return "Tú";
  return state.contacts.get(id)?.displayName ?? "Alguien que no conoces";
}

function roomDetail(r) {
  if (r.left) return "Ya no estás en esta sala";
  return `${r.members.length} ${r.members.length === 1 ? "miembro" : "miembros"}`;
}

function roomItem(r) {
  const key = ROOM + r.id;
  const li = document.createElement("li");
  li.className = "contact room" + (r.left ? " offline" : "") + (key === state.current ? " selected" : "");
  li.title = r.members.map(memberName).join(", ");

  const icon = document.createElement("span");
  icon.className = "room-icon";
  icon.textContent = "👥";
  const text = document.createElement("div");
  text.className = "text";
  const name = document.createElement("span");
  name.className = "name";
  name.textContent = r.name;
  const detail = document.createElement("span");
  detail.className = "detail";
  detail.textContent = roomDetail(r);
  text.append(name, detail);
  li.append(icon, text);
  if (r.unread > 0) {
    const badge = document.createElement("span");
    badge.className = "badge";
    badge.textContent = r.unread > 99 ? "99+" : r.unread;
    li.append(badge);
  }
  li.addEventListener("click", () => openChat(key));
  return li;
}

// ---------- Render: conversación ----------

// showFor muestra los botones de contacto o los de sala.
function showFor(room) {
  for (const el of document.querySelectorAll(".contact-only")) el.hidden = room;
  for (const el of document.querySelectorAll(".room-only")) el.hidden = !room;
}

function renderRoomHeader(r) {
  showFor(true);
  $("peer-name").textContent = r.name;
  $("peer-detail").textContent = r.members.map(memberName).join(", ");
  $("peer-dot").hidden = true;
  $("identity-note").hidden = true;
  $("incompatible-note").hidden = true;
  for (const id of ["room-rename-btn", "room-leave-btn"]) $(id).disabled = r.left;
  syncComposer();
  const note = $("offline-note");
  note.hidden = !r.left;
  note.textContent = "Ya no estás en esta sala: no recibes sus mensajes. Para volver, pide a un miembro que te agregue.";
}

function renderHeader() {
  if (roomId(state.current)) {
    const r = currentRoom();
    if (r) renderRoomHeader(r);
    return;
  }
  const c = state.contacts.get(state.current);
  if (!c) return;
  showFor(false);
  $("peer-dot").hidden = false;
  syncComposer(); // a un contacto incompatible no se le puede escribir
  $("incompatible-note").hidden = !c.incompatible;
  $("incompatible-note").textContent = incompatibleText(c);
  $("peer-name").textContent = c.displayName;
  let detail = c.detail;
  if (c.online) detail += ` · ${statusLabel(c.status, c.statusText)}`;
  if (c.appVersion && c.appVersion !== state.self.version) {
    detail += ` · versión ${c.appVersion} (la tuya es ${state.self.version})`;
  }
  $("peer-detail").textContent = detail;
  $("peer-dot").className = dotClass(c.online, c.status);
  $("remove-contact-btn").hidden = c.online;
  const note = $("offline-note");
  note.hidden = c.online;
  note.textContent = `${c.displayName} está desconectado. Los mensajes que envíes se entregarán cuando se conecte.`;
  $("identity-note").hidden = !c.identityChanged;
  $("identity-note-text").textContent = `La identidad de ${c.displayName} cambió. Si reinstaló LanChat es normal; si no, alguien podría estar haciéndose pasar por ese equipo. Hasta que confíes en la nueva identidad no se le envía nada.`;
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
  if (m.kind === "files" && m.transfer && !m.deleted) return withActions(transferCard(m), m);
  const div = document.createElement("div");
  // Un mensaje eliminado no muestra marcas de entrega ni de lectura.
  div.className = m.deleted
    ? `msg ${m.outgoing ? "out" : "in"} deleted`
    : msgClass(m) + (m.kind === "event" ? " event" : "");
  div.dataset.id = m.id;
  if (m.kind === "event") {
    div.append(document.createTextNode(m.body), metaElement(m));
    return div;
  }
  if (m.deleted) {
    const text = m.outgoing ? "🚫 Eliminaste este mensaje" : "🚫 Se eliminó este mensaje";
    div.append(document.createTextNode(text), metaElement(m));
    return div;
  }
  if (m.roomId && !m.outgoing) {
    const author = document.createElement("span");
    author.className = "author";
    author.textContent = memberName(m.peerId);
    div.append(author);
  }
  if (m.broadcast) {
    const tag = document.createElement("span");
    tag.className = "broadcast-tag";
    tag.textContent = "📢 Mensaje a varios";
    div.append(tag);
  }
  if (m.forwarded) div.append(forwardedTag());
  div.append(document.createTextNode(m.body), metaElement(m));
  return withActions(div, m);
}

function forwardedTag() {
  const tag = document.createElement("span");
  tag.className = "broadcast-tag forwarded-tag";
  tag.textContent = "↪ Reenviado";
  return tag;
}

// ---------- Selector de emojis ----------

// EMOJI_GROUPS (emoji.js) trae todos los emojis de Unicode por grupo. Cada
// sistema sabe dibujar solo una parte (Windows 10 no tiene los más nuevos ni
// las banderas de países): el selector muestra los que aquí sí se ven.

const RECENT_KEY = "lanchat.recentEmoji";
// MAX_RECENT: cuántos emojis recuerda la pestaña Recientes (cuatro renglones).
const MAX_RECENT = 32;
const RECENT_TAB = { name: "Recientes", icon: "🕒" };
// EMOJI_FONTS: las fuentes de emoji de Windows, Android y macOS.
const EMOJI_FONTS = '"Segoe UI Emoji", "Noto Color Emoji", "Apple Color Emoji", sans-serif';
// Una secuencia que el sistema no conoce se dibuja como varios emojis
// seguidos: sale más ancha que uno solo.
const MAX_EMOJI_WIDTH = 1.4;

let emojiTab = null; // null = aún no se abrió
const drawableCache = new Map();
let probe = null; // lienzo para probar qué emojis se pueden dibujar

function emojiProbe() {
  if (probe) return probe;
  const size = 28;
  const ctx = document.createElement("canvas").getContext("2d", { willReadFrequently: true });
  ctx.canvas.width = ctx.canvas.height = size;
  ctx.font = `20px ${EMOJI_FONTS}`;
  ctx.textBaseline = "top";
  const draw = (text) => {
    ctx.clearRect(0, 0, size, size);
    ctx.fillStyle = "#000";
    ctx.fillText(text, 0, 2);
    // Resume el dibujo: cuánta tinta tiene, si tiene color y una huella.
    const d = ctx.getImageData(0, 0, size, size).data;
    let ink = 0, color = false, hash = 0;
    for (let i = 0; i < d.length; i += 4) {
      if (d[i + 3] === 0) continue;
      ink++;
      if (d[i] !== d[i + 1] || d[i + 1] !== d[i + 2]) color = true;
      hash = (hash * 31 + i + d[i] + d[i + 1] * 3 + d[i + 2] * 7 + d[i + 3] * 11) | 0;
    }
    return { ink, color, hash };
  };
  probe = {
    draw,
    width: (text) => ctx.measureText(text).width,
    base: ctx.measureText("😀").width,
    // Así se ve un carácter que ninguna fuente tiene (el "cuadrito").
    missing: draw(String.fromCodePoint(0xFFFF)).hash,
  };
  return probe;
}

// needsColor: las banderas de países (dos letras indicadoras) y las de
// regiones (con etiquetas) solo valen si salen a color; sin la fuente se ven
// como letras sueltas o como una bandera negra.
function needsColor(emoji) {
  const first = emoji.codePointAt(0);
  return (first >= 0x1F1E6 && first <= 0x1F1FF) || /[\u{E0020}-\u{E007F}]/u.test(emoji);
}

// drawable indica si este sistema sabe dibujar el emoji.
function drawable(emoji) {
  let ok = drawableCache.get(emoji);
  if (ok !== undefined) return ok;
  const p = emojiProbe();
  const w = p.width(emoji);
  if (w === 0 || w > p.base * MAX_EMOJI_WIDTH) {
    ok = false;
  } else {
    const d = p.draw(emoji);
    ok = d.ink > 0 && (d.color || (!needsColor(emoji) && d.hash !== p.missing));
  }
  drawableCache.set(emoji, ok);
  return ok;
}

function recentEmoji() {
  try {
    const list = JSON.parse(localStorage.getItem(RECENT_KEY) ?? "[]");
    return Array.isArray(list) ? list.filter((e) => typeof e === "string") : [];
  } catch {
    return [];
  }
}

function rememberEmoji(emoji) {
  const list = [emoji, ...recentEmoji().filter((e) => e !== emoji)].slice(0, MAX_RECENT);
  try {
    localStorage.setItem(RECENT_KEY, JSON.stringify(list));
  } catch {
    // sin almacenamiento no hay Recientes
  }
}

function renderEmojiPanel() {
  const tabs = [RECENT_TAB, ...EMOJI_GROUPS];
  $("emoji-tabs").replaceChildren(...tabs.map((g) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "emoji-tab" + (g === emojiTab ? " selected" : "");
    b.textContent = g.icon;
    b.title = g.name;
    b.setAttribute("role", "tab");
    b.setAttribute("aria-selected", g === emojiTab);
    b.addEventListener("click", () => {
      emojiTab = g;
      renderEmojiPanel();
    });
    return b;
  }));
  $("emoji-title").textContent = emojiTab.name;
  const list = (emojiTab === RECENT_TAB ? recentEmoji() : emojiTab.list).filter(drawable);
  const grid = $("emoji-grid");
  grid.replaceChildren(...list.map((emoji) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "emoji-cell";
    b.textContent = emoji;
    b.addEventListener("click", () => insertEmoji(emoji));
    return b;
  }));
  if (list.length === 0) {
    const empty = document.createElement("div");
    empty.className = "emoji-empty";
    empty.textContent = emojiTab === RECENT_TAB
      ? "Aquí aparecerán los emojis que uses."
      : "Este equipo no puede mostrar los emojis de este grupo.";
    grid.append(empty);
  }
  grid.scrollTop = 0;
}

function openEmojiPanel() {
  // La primera vez: Recientes si ya hay alguno; si no, las caritas.
  emojiTab ??= recentEmoji().some(drawable) ? RECENT_TAB : EMOJI_GROUPS[0];
  renderEmojiPanel();
  // En el teléfono se esconde el teclado: si no, tapa el panel.
  if (state.mobile) $("input").blur();
  $("emoji-panel").hidden = false;
  $("emoji-btn").setAttribute("aria-expanded", "true");
}

function closeEmojiPanel() {
  $("emoji-panel").hidden = true;
  $("emoji-btn").setAttribute("aria-expanded", "false");
}

// insertEmoji escribe el emoji donde está el cursor (la caja lo recuerda
// aunque no tenga el foco) y deja el panel abierto para poner varios.
function insertEmoji(emoji) {
  const input = $("input");
  input.setRangeText(emoji, input.selectionStart, input.selectionEnd, "end");
  input.dispatchEvent(new Event("input")); // ajusta la altura de la caja
  // En el teléfono, darle el foco abriría el teclado encima del panel.
  if (!state.mobile) input.focus();
  rememberEmoji(emoji);
}

// ---------- Reacciones y eliminar para todos ----------

// Las reacciones rápidas son las de WhatsApp; "+" muestra el resto. Todas
// deben pasar protocol.ValidateReaction (lo comprueba una prueba).
const QUICK_REACTIONS = ["👍", "❤️", "😂", "😮", "😢", "🙏"];
const MORE_REACTIONS = ["👏", "🎉", "🔥", "✅", "👌", "💪", "🙌", "😊", "😍", "🤔", "😅", "😎",
  "😴", "😡", "👀", "💯", "⭐", "☕", "🍕", "🎂", "📌", "⏰", "👎", "❌"];

// myReaction es el emoji con que este usuario reaccionó ("" = ninguno).
const myReaction = (m) => (m.reactions ?? []).find((r) => r.peer === "")?.emoji ?? "";

// canDelete: un mensaje propio dentro del plazo se elimina para todos, si es
// de texto o un envío de archivos que ya terminó (descargado o que no llegó
// a nada). Lo descargado se queda en la carpeta del otro.
function canDelete(m) {
  if (!m.outgoing || m.deleted || Date.now() - m.at > state.limits.deleteWindowMs) return false;
  return m.kind === "text" || canDeleteHere(m) || (m.kind === "files" && m.transfer?.state === "completed");
}

// canDeleteHere: un envío de archivos que no llegó a nada se borra de este
// equipo (enviado o recibido, sin plazo).
const canDeleteHere = (m) => m.kind === "files" && !m.deleted && DELETABLE_TRANSFERS.has(m.transfer?.state);

// canEdit: un mensaje de texto propio se edita dentro del mismo plazo que
// para eliminarlo para todos (protocol.DeleteWindow).
function canEdit(m) {
  if (!m.outgoing || m.deleted || m.kind !== "text" || Date.now() - m.at > state.limits.deleteWindowMs) return false;
  return !m.roomId || !state.rooms.get(m.roomId)?.left;
}

// canForward: se reenvían los textos y los envíos de archivos propios o ya
// descargados (el servidor comprueba que los archivos sigan en el equipo).
function canForward(m) {
  if (m.deleted) return false;
  if (m.kind === "text") return true;
  return m.kind === "files" && Boolean(m.transfer) && (m.transfer.outgoing || m.transfer.state === "completed");
}

// withActions agrega al globo el botón que abre el menú del mensaje.
function withActions(div, m) {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "msg-act";
  btn.textContent = "☺";
  btn.title = "Reaccionar";
  btn.setAttribute("aria-label", "Reaccionar u otras opciones del mensaje");
  btn.addEventListener("click", (e) => {
    e.stopPropagation();
    openMsgMenu(m.id, btn);
  });
  div.append(btn);
  return div;
}

// findMessage busca en la conversación abierta (el mensaje pudo cambiar
// desde que se dibujó).
const findMessage = (id) => state.messages.get(state.current)?.list.find((x) => x.id === id);

function closeMsgMenu() {
  $("msg-menu").hidden = true;
  document.querySelector(".msg-act.open")?.classList.remove("open");
}

function openMsgMenu(id, anchor, showMore = false) {
  const m = findMessage(id);
  const menu = $("msg-menu");
  if (!m || m.deleted || (!menu.hidden && menu.dataset.id === id && !showMore)) return closeMsgMenu();
  closeMsgMenu();
  menu.dataset.id = id;
  const mine = myReaction(m);
  const row = document.createElement("div");
  row.className = "emoji-row";
  for (const emoji of showMore ? [...QUICK_REACTIONS, ...MORE_REACTIONS] : QUICK_REACTIONS) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "emoji" + (emoji === mine ? " mine" : "");
    b.textContent = emoji;
    b.title = emoji === mine ? "Quitar mi reacción" : "Reaccionar";
    b.addEventListener("click", () => react(m, emoji));
    row.append(b);
  }
  if (!showMore) {
    const more = document.createElement("button");
    more.type = "button";
    more.className = "emoji more";
    more.textContent = "＋";
    more.title = "Más reacciones";
    more.addEventListener("click", (e) => {
      e.stopPropagation();
      openMsgMenu(id, anchor, true);
    });
    row.append(more);
  }
  menu.replaceChildren(row);
  const item = (label, onClick, danger = false) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "menu-item" + (danger ? " danger" : "");
    b.textContent = label;
    b.addEventListener("click", onClick);
    menu.append(b);
  };
  if (canEdit(m)) item("Editar", () => startEdit(m));
  if (canForward(m)) {
    item("Reenviar", () => {
      closeMsgMenu();
      openForward([m.id]);
    });
  }
  item("Seleccionar", () => startSelect(m));
  if (canDelete(m)) item("Eliminar para todos", () => deleteForEveryone(m), true);
  if (canDeleteHere(m)) {
    item("Borrar de este equipo", () => {
      closeMsgMenu();
      const who = m.outgoing ? memberName(m.peerId) : "el remitente";
      transferAction("delete", m.id, `¿Borrar este envío de este equipo, con sus vistas previas? ${who} conserva el suyo.`);
    }, true);
  }
  menu.hidden = false;
  anchor.classList.add("open");
  // Junto al botón, sin salirse de la ventana.
  const a = anchor.getBoundingClientRect();
  const w = menu.offsetWidth;
  const h = menu.offsetHeight;
  menu.style.left = `${Math.max(8, Math.min(a.left + a.width / 2 - w / 2, innerWidth - w - 8))}px`;
  menu.style.top = `${a.bottom + h + 8 > innerHeight ? Math.max(8, a.top - h - 6) : a.bottom + 6}px`;
}

// react pone la reacción del usuario; elegir la que ya tenía la quita.
async function react(m, emoji) {
  closeMsgMenu();
  try {
    addMessage(await api.react(m.id, emoji === myReaction(m) ? "" : emoji));
  } catch (e) {
    showBanner(e.message, 5000);
  }
}

async function deleteForEveryone(m) {
  closeMsgMenu();
  const where = m.roomId ? "todos los miembros de la sala" : memberName(m.peerId);
  const what = m.kind === "files" ? "este envío" : "este mensaje";
  const kept = m.transfer?.state === "completed" ? " Los archivos que ya descargó se quedan en su carpeta." : "";
  if (!confirm(`¿Eliminar ${what} para todos? También se borrará en el equipo de ${where}.${kept}`)) return;
  try {
    addMessage(await api.deleteMessage(m.id));
  } catch (e) {
    showBanner(e.message, 5000);
  }
}

// reactionsElement dibuja las reacciones bajo el mensaje, agrupadas por
// emoji; null si no tiene.
function reactionsElement(m) {
  if (m.deleted || !m.reactions?.length) return null;
  const groups = new Map(); // emoji -> quiénes
  for (const r of m.reactions) groups.set(r.emoji, [...(groups.get(r.emoji) ?? []), r.peer]);
  const box = document.createElement("div");
  box.className = `reactions ${m.outgoing ? "out" : "in"}`;
  for (const [emoji, peers] of [...groups].sort((x, y) => y[1].length - x[1].length)) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "reaction" + (peers.includes("") ? " mine" : "");
    b.textContent = peers.length > 1 ? `${emoji} ${peers.length}` : emoji;
    b.title = peers.map((p) => (p === "" ? "Tú" : memberName(p))).join(", ");
    b.addEventListener("click", () => react(m, emoji));
    box.append(b);
  }
  return box;
}

// msgClass: dirección, estado de entrega y si el destinatario ya lo leyó.
function msgClass(m) {
  return `msg ${m.outgoing ? "out" : "in"} ${m.status}` + (m.readAt ? " read" : "");
}

function metaElement(m) {
  const meta = document.createElement("span");
  meta.className = "meta";
  meta.textContent = timeFmt.format(new Date(m.at));
  if (m.edited && !m.deleted) meta.prepend("editado · ");
  if (m.mergedFrom) meta.prepend(mergedElement(m));
  if (m.status === "pending" && m.roomId) meta.title = "Pendiente: falta entregarlo a algún miembro desconectado";
  else if (m.status === "pending") meta.title = "Pendiente: se entregará cuando el contacto se conecte";
  else if (m.outgoing && m.readAt) meta.title = `Leído: ${dayLabel(m.readAt)} ${timeFmt.format(new Date(m.readAt))}`;
  else if (m.outgoing) meta.title = "Entregado";
  return meta;
}

// mergedElement: el mensaje era de otro contacto (una identidad vieja que se
// quitó) y se combinó aquí; se dice de quién, para que no pase por escrito
// por este.
function mergedElement(m) {
  const span = document.createElement("span");
  span.className = "merged";
  span.textContent = `⤷ Combinado de ${m.mergedFrom} · `;
  span.title = m.outgoing
    ? `Lo enviaste a otro contacto (${m.mergedFrom}) que quitaste y combinaste con esta conversación.`
    : `Lo escribió otro contacto (${m.mergedFrom}) que quitaste y combinaste con esta conversación.`;
  return span;
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
  // Uno combinado fue de otro contacto: se nombra a ese.
  const who = m.mergedFrom?.split(" · ")[0] || (state.contacts.get(m.peerId)?.displayName ?? "El contacto");
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

// DELETABLE_TRANSFERS: estados en que una oferta no llegó a nada; se borra
// desde el menú del mensaje (app.DeleteTransfer y app.DeleteForEveryone).
const DELETABLE_TRANSFERS = new Set(["canceled", "rejected", "expired"]);

function transferCard(m) {
  const t = m.transfer;
  const div = document.createElement("div");
  div.className = `${msgClass(m)} files ${t.state}`;
  div.dataset.id = m.id;
  if (m.forwarded) div.append(forwardedTag());

  const title = document.createElement("div");
  title.className = "files-title";
  title.textContent = `📎 ${t.files.length === 1 ? "1 archivo" : `${t.files.length} archivos`} · ${fmtSize(t.total)}`;
  div.append(title);

  const shown = previews(t);
  if (shown) div.append(shown);
  div.append(fileList(t));

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

// MAX_PREVIEWS: miniaturas que muestra una tarjeta; con más, "+N" en la última.
const MAX_PREVIEWS = 4;

// previews muestra las miniaturas de las imágenes de una oferta, como en
// WhatsApp: una sola, grande; varias, en cuadrícula. Llegan con la oferta, así
// que se ven antes de aceptar. Tocar abre la imagen completa si ya se puede
// (enviada, o recibida y terminada).
function previews(t) {
  if (!state.self.previews) return null;
  const withThumb = t.files.filter((f) => f.thumb);
  if (withThumb.length === 0) return null;
  const shown = withThumb.slice(0, MAX_PREVIEWS);
  const box = document.createElement("div");
  box.className = `previews n${shown.length}`;
  shown.forEach((f, i) => {
    const cell = document.createElement("button");
    cell.type = "button";
    cell.className = "preview";
    const img = document.createElement("img");
    img.src = `/api/files/thumb?${new URLSearchParams({ id: t.id, index: f.index })}`;
    img.alt = f.name;
    img.loading = "lazy";
    cell.append(img);
    const extra = withThumb.length - shown.length;
    if (i === shown.length - 1 && extra > 0) {
      const more = document.createElement("span");
      more.className = "preview-more";
      more.textContent = `+${extra}`;
      cell.append(more);
    }
    if (f.view) {
      cell.title = `Ver ${f.name}`;
      cell.addEventListener("click", () => openViewer(t, f));
    } else {
      cell.title = t.outgoing ? f.name : "Acepta para descargarla y verla completa";
      cell.disabled = true;
    }
    box.append(cell);
  });
  return box;
}

// openViewer muestra la imagen completa, leída directo del archivo.
function openViewer(t, f) {
  const img = $("viewer-img");
  img.onerror = () => {
    $("viewer").close();
    showBanner("No se pudo abrir la imagen: el archivo ya no está o no es una imagen.", 5000);
  };
  img.src = `/api/files/view?${new URLSearchParams({ id: t.id, index: f.index })}`;
  img.alt = f.name;
  $("viewer-name").textContent = f.savedName || f.name;
  $("viewer").showModal();
}

// MAX_FILE_ROWS: filas que muestra una tarjeta antes de resumir "y N más".
const MAX_FILE_ROWS = 10;

// fileList muestra los archivos sueltos y, de cada carpeta, una sola fila.
// Sin escritorio no se pueden abrir carpetas: se lista cada archivo.
function fileList(t) {
  const ul = document.createElement("ul");
  ul.className = "file-list";
  const rows = [];
  const folders = new Map(); // carpeta raíz -> { count, size, done }
  for (const f of t.files) {
    if (!f.dir || state.mobile) {
      rows.push({ file: f });
      continue;
    }
    const root = f.dir.split("/")[0];
    if (!folders.has(root)) {
      folders.set(root, { count: 0, size: 0, done: 0 });
      rows.push({ folder: root });
    }
    const info = folders.get(root);
    info.count++;
    info.size += f.size;
    if (f.done) info.done++;
  }

  for (const row of rows.slice(0, MAX_FILE_ROWS)) {
    const li = document.createElement("li");
    const name = document.createElement("span");
    name.className = "file-name";
    const size = document.createElement("span");
    size.className = "file-size";
    if (row.folder) {
      const info = folders.get(row.folder);
      name.textContent = `📁 ${row.folder}`;
      size.textContent = `${info.count} archivos · ${fmtSize(info.size)}`;
      li.append(name, size);
      if (!t.outgoing && info.done > 0 && !state.mobile) {
        li.append(textButton("Abrir carpeta", () =>
          api.openDir(t.id, row.folder).catch((e) => showBanner(e.message, 5000))));
      }
    } else {
      const f = row.file;
      name.textContent = (f.dir ? `${f.dir}/` : "") + (f.savedName || f.name);
      name.title = f.savedName && f.savedName !== f.name ? `Original: ${f.name}` : f.name;
      size.textContent = fmtSize(f.size) + (f.done && t.state !== "completed" ? " ✓" : "");
      li.append(name, size);
      if (!t.outgoing && f.done) {
        li.append(textButton("Abrir", () => openReceived(t, f, false)));
        if (!state.mobile) li.append(textButton("Mostrar en carpeta", () => openReceived(t, f, true)));
      }
    }
    ul.append(li);
  }
  if (rows.length > MAX_FILE_ROWS) {
    const li = document.createElement("li");
    li.className = "file-size";
    li.textContent = `y ${rows.length - MAX_FILE_ROWS} más…`;
    ul.append(li);
  }
  return ul;
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

// pickAndSend abre el selector de Windows (archivos o carpeta) y los ofrece.
async function pickAndSend(btn, pick) {
  btn.disabled = true;
  try {
    const m = await pick(state.current);
    if (m) addMessage(m);
  } catch (e) {
    showBanner(e.message, 5000);
  } finally {
    btn.disabled = false;
  }
}

// MAX_OFFER_FILES coincide con protocol.MaxOfferFiles.
const MAX_OFFER_FILES = 1000;

// droppedFiles recorre lo que se soltó (archivos y carpetas, en cualquier
// nivel) y devuelve [{file, dir}] con la subcarpeta de cada archivo.
async function droppedFiles(entries) {
  const out = [];
  const walk = async (entry, dir) => {
    if (out.length > MAX_OFFER_FILES) return;
    if (entry.isFile) {
      out.push({ file: await new Promise((ok, fail) => entry.file(ok, fail)), dir });
    } else if (entry.isDirectory) {
      const sub = dir ? `${dir}/${entry.name}` : entry.name;
      const reader = entry.createReader();
      for (;;) { // readEntries entrega por tandas hasta devolver una vacía
        const batch = await new Promise((ok, fail) => reader.readEntries(ok, fail));
        if (batch.length === 0) break;
        for (const e of batch) await walk(e, sub);
      }
    }
  };
  for (const e of entries) await walk(e, "");
  return out;
}

async function uploadDropped(dt) {
  if (!state.current) return showBanner("Elige primero a quién enviarle los archivos.", 4000);
  // Las entradas se toman ya: el DataTransfer deja de servir tras el evento.
  const entries = [...dt.items].map((i) => i.webkitGetAsEntry?.()).filter(Boolean);
  const loose = [...dt.files];
  showBanner("Preparando archivos…");
  let items;
  try {
    items = entries.length ? await droppedFiles(entries) : loose.map((file) => ({ file, dir: "" }));
  } catch (e) {
    return showBanner(`No se pudieron leer los archivos: ${e.message}`, 5000);
  }
  await uploadItems(items);
}

// uploadPicked envía lo elegido con el selector del sistema (sin escritorio).
function uploadPicked(input) {
  const items = [...input.files].map((file) => ({ file, dir: "" }));
  input.value = ""; // para poder elegir otra vez el mismo archivo
  if (state.current && items.length > 0) uploadItems(items);
}

// uploadItems copia los archivos ([{file, dir}]) a este equipo y los ofrece
// en la conversación abierta.
async function uploadItems(items) {
  if (items.length === 0) return showBanner("No hay archivos para enviar (¿carpetas vacías?).", 5000);
  if (items.length > MAX_OFFER_FILES) {
    return showBanner(`Son más de ${MAX_OFFER_FILES} archivos; comprímelos en un .zip o envíalos en partes.`, 6000);
  }
  showBanner(`Preparando ${items.length === 1 ? items[0].file.name : `${items.length} archivos`}…`);
  try {
    addMessage(await api.upload(state.current, uploadForm(items)));
    hideBanner();
  } catch (e) {
    showBanner(e.message, 5000);
  }
}

function uploadForm(items) {
  const form = new FormData();
  for (const { file, dir } of items) form.append(dir ? `dir:${dir}` : "files", file, file.name);
  return form;
}

// ---------- Compartir desde otras apps (Android) ----------

// lanchatShare lo llama la app de Android cuando el usuario comparte archivos
// (o un texto) con LanChat desde otra app: {files: [{name, size, url}], text}.
// Cada url la sirve la app (MainActivity) mientras el envío esté pendiente.
// Se pregunta a quién enviarlos.
let pendingShare = null;
window.lanchatShare = (share) => {
  if (!state.loaded) {
    pendingShare = share;
    return;
  }
  openShare(share);
};

let currentShare = null;

function openShare(share) {
  const files = share.files ?? [];
  const text = share.text ?? "";
  if (files.length === 0 && !text) return shareFinished();
  if (files.length > MAX_OFFER_FILES) {
    showBanner(`Son más de ${MAX_OFFER_FILES} archivos; compártelos en partes.`, 6000);
    return shareFinished();
  }
  currentShare = share;
  // Otro diálogo abierto se cierra; el de compartir se reutiliza (cerrarlo
  // avisaría a la app que este envío terminó).
  const open = document.querySelector("dialog[open]");
  if (open && open.id !== "share-dialog") open.close();
  const total = files.reduce((n, f) => n + Math.max(f.size, 0), 0);
  $("share-title").textContent = files.length === 0 ? "Enviar mensaje"
    : files.length === 1 ? "Enviar 1 archivo" : `Enviar ${files.length} archivos`;
  const names = files.slice(0, 3).map((f) => f.name).join(", ");
  const more = files.length > 3 ? ` y ${files.length - 3} más` : "";
  $("share-files").textContent = files.length ? `${names}${more}${total ? ` · ${fmtSize(total)}` : ""}` : "";
  $("share-files").hidden = files.length === 0;
  $("share-list").replaceChildren(...sortedContacts().map(pickItem));
  // Un texto compartido junto a archivos (p. ej. el pie de una foto) va como mensaje.
  $("share-text-box").hidden = !text;
  $("share-text").value = text;
  $("share-error").hidden = true;
  $("share-send").disabled = false;
  if (!$("share-dialog").open) $("share-dialog").showModal();
}

// shareFinished avisa a la app que ya no hace falta servir los archivos.
function shareFinished() {
  currentShare = null;
  window.LanChatAndroid?.shareDone?.();
}

async function sendShare(ev) {
  if (ev.submitter?.value !== "save") return;
  ev.preventDefault();
  const share = currentShare;
  const peers = [...$("share-list").querySelectorAll("input:checked")].map((b) => b.value);
  const err = $("share-error");
  err.hidden = true;
  if (!share) return $("share-dialog").close();
  if (peers.length === 0) {
    err.textContent = "Elige al menos un contacto.";
    err.hidden = false;
    return;
  }
  const text = $("share-text-box").hidden ? "" : $("share-text").value.trim();
  $("share-send").disabled = true;
  try {
    let items = [];
    if (share.files?.length) {
      showBanner("Preparando los archivos…");
      items = await Promise.all(share.files.map(async (f) => {
        const resp = await fetch(f.url);
        if (!resp.ok) throw new Error(`No se pudo leer ${f.name}`);
        return { file: new File([await resp.blob()], f.name), dir: "" };
      }));
    }
    for (const [i, peer] of peers.entries()) {
      if (items.length) {
        showBanner(peers.length === 1 ? "Enviando…" : `Enviando a ${i + 1} de ${peers.length}…`);
        addMessage(await api.upload(peer, uploadForm(items)));
      }
      if (text) addMessage(await api.send(peer, text));
    }
    hideBanner();
    $("share-dialog").close();
    if (peers.length === 1) openChat(peers[0]);
    else showBanner(`Enviado a ${peers.length} contactos.`, 4000);
  } catch (e) {
    hideBanner();
    err.textContent = e.message;
    err.hidden = false;
    $("share-send").disabled = false;
  }
}

function selectShare(which) {
  for (const box of $("share-list").querySelectorAll("input")) {
    box.checked = which === "online" && box.dataset.online === "true";
  }
}

function renderMessages({ keepScroll = false, toBottom = false } = {}) {
  const box = $("messages");
  const conv = state.messages.get(state.current);
  const prevHeight = box.scrollHeight;
  const prevTop = box.scrollTop;
  const nearBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 80;

  // Los seleccionados que ya no están (o se eliminaron) dejan de estarlo.
  if (state.selecting) {
    for (const id of state.selected) {
      const m = conv?.list.find((x) => x.id === id);
      if (!m || !selectable(m)) state.selected.delete(id);
    }
    if (state.selected.size === 0) {
      state.selecting = false;
      syncComposer();
    }
  }
  box.classList.toggle("selecting", state.selecting);

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
    const el = messageElement(m);
    if (state.selecting && selectable(m)) {
      el.classList.add("selectable");
      el.classList.toggle("selected", state.selected.has(m.id));
    }
    box.append(el);
    const reactions = reactionsElement(m);
    if (reactions) box.append(reactions);
  }
  closeMsgMenu();
  if (state.selecting) updateSelectBar();

  if (keepScroll) box.scrollTop = box.scrollHeight - prevHeight + prevTop;
  else if (toBottom || nearBottom) box.scrollTop = box.scrollHeight;
}

// ---------- Acciones ----------

// setMenu abre o cierra el menú ⋯ de la conversación (ventana angosta).
function setMenu(open) {
  $("chat-actions").classList.toggle("open", open);
  $("more-btn").setAttribute("aria-expanded", open);
}

// NARROW coincide con la ventana angosta de style.css (una columna).
const NARROW = window.matchMedia("(max-width: 640px)");

// lanchatBack lo llama el botón Atrás de Android (MainActivity): cierra lo
// que esté abierto encima y devuelve true; con false la app pasa a segundo plano.
window.lanchatBack = () => {
  const dialog = document.querySelector("dialog[open]");
  if (dialog) {
    dialog.close();
    return true;
  }
  if ($("chat-actions").classList.contains("open")) {
    setMenu(false);
    return true;
  }
  if (!$("msg-menu").hidden) {
    closeMsgMenu();
    return true;
  }
  if (state.selecting) {
    endSelect();
    return true;
  }
  if (state.editing) {
    cancelEdit();
    return true;
  }
  if (!$("emoji-panel").hidden) {
    closeEmojiPanel();
    return true;
  }
  if (state.current && NARROW.matches) {
    closeChat();
    return true;
  }
  return false;
};

async function openChat(id) {
  setMenu(false);
  closeEmojiPanel();
  endSelect();
  cancelEdit();
  window.LanChatAndroid?.chatOpened(id); // en el teléfono, quita su aviso
  state.current = id;
  $("app").classList.add("chatting");
  $("empty").hidden = true;
  $("chat-header").hidden = false;
  $("send-error").hidden = true;
  renderHeader();
  renderContacts();
  renderMessages({ toBottom: true });
  reportPresence();
  if (!state.messages.has(id)) await loadHistory(id);
  markReadIfVisible();
  // En el teléfono el foco abriría el teclado y taparía los mensajes.
  if (!state.mobile) $("input").focus();
}

function closeChat() {
  setMenu(false);
  endSelect();
  cancelEdit();
  state.current = null;
  $("app").classList.remove("chatting");
  reportPresence();
}

// showEmpty deja la ventana sin conversación abierta.
function showEmpty() {
  closeChat();
  $("empty").hidden = false;
  $("chat-header").hidden = true;
  $("composer").hidden = true;
  $("edit-bar").hidden = true;
  $("select-bar").hidden = true;
  $("offline-note").hidden = true;
  $("identity-note").hidden = true;
  $("incompatible-note").hidden = true;
  $("messages").replaceChildren();
  renderContacts();
}

async function loadHistory(peer, before) {
  let page;
  try {
    const room = roomId(peer);
    page = await (room ? api.roomHistory(room, before) : api.history(peer, before));
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
  const key = convKey(m);
  const conv = state.messages.get(key);
  if (!conv) return; // la conversación se cargará completa al abrirla
  const i = conv.list.findIndex((x) => x.id === m.id);
  if (i >= 0) {
    const old = conv.list[i];
    // Un evento atrasado (p. ej. la entrega) no revive un mensaje eliminado.
    if (old.deleted && !m.deleted) return;
    m.transfer ??= old.transfer; // conservar la oferta si el evento no la trae
    m.reactions ??= old.reactions; // y las reacciones
    // La respuesta del envío puede llegar después del evento de entrega: el
    // estado nunca retrocede.
    if (old.status === "delivered") m.status = "delivered";
    m.readAt ||= old.readAt;
    conv.list[i] = m;
  } else {
    conv.list.push(m);
  }
  if (key === state.current) renderMessages({ toBottom: m.outgoing });
}

async function sendMessage() {
  const input = $("input");
  const body = input.value;
  const err = $("send-error");
  err.hidden = true;
  closeEmojiPanel();
  if (!state.current) return;
  if (encoder.encode(body).length > state.limits.maxMessageBytes) {
    err.textContent = `El mensaje es demasiado largo (máximo ${state.limits.maxMessageBytes / 1024} KB).`;
    err.hidden = false;
    return;
  }
  if (state.editing) return saveEdit(body);
  if (!body.trim()) return;
  try {
    const room = roomId(state.current);
    const m = await (room ? api.roomSend(room, body) : api.send(state.current, body));
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
  const r = currentRoom();
  if (r && r.unread > 0 && windowFocused()) {
    r.unread = 0; // el servidor confirmará con un evento "room"
    renderContacts();
    api.room("read", r.id).catch(() => {});
    return;
  }
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

// ---------- Editar un mensaje ----------

// startEdit pone el texto del mensaje en la caja de escribir; Enter guarda
// y Esc cancela (como en WhatsApp).
function startEdit(m) {
  closeMsgMenu();
  endSelect();
  const input = $("input");
  if (!state.editing) state.editDraft = input.value;
  state.editing = m.id;
  $("edit-original").textContent = m.body;
  $("send-error").hidden = true;
  input.value = m.body;
  autoGrow();
  syncComposer();
  input.focus();
  input.setSelectionRange(input.value.length, input.value.length);
}

// cancelEdit deja la caja como estaba antes de editar.
function cancelEdit() {
  if (!state.editing) return;
  state.editing = null;
  $("input").value = state.editDraft;
  state.editDraft = "";
  autoGrow();
  syncComposer();
}

async function saveEdit(body) {
  const err = $("send-error");
  const m = findMessage(state.editing);
  if (!m) return cancelEdit();
  if (!body.trim()) {
    err.textContent = "El mensaje no puede quedar vacío. Para quitarlo, usa «Eliminar para todos».";
    err.hidden = false;
    return;
  }
  if (body === m.body) return cancelEdit();
  try {
    addMessage(await api.editMessage(m.id, body));
    cancelEdit();
  } catch (e) {
    err.textContent = e.message;
    err.hidden = false;
  }
}

// canWrite: se puede escribir en la conversación abierta.
function canWrite() {
  if (roomId(state.current)) return Boolean(currentRoom() && !currentRoom().left);
  const c = state.contacts.get(state.current);
  return Boolean(c && !c.incompatible);
}

// syncComposer muestra la caja de escribir (con la barra de edición) o, al
// seleccionar mensajes, la barra de la selección.
function syncComposer() {
  $("select-bar").hidden = !state.selecting;
  $("composer").hidden = state.selecting || !canWrite();
  $("edit-bar").hidden = $("composer").hidden || !state.editing;
  $("send-btn").textContent = state.editing ? "Guardar" : "Enviar";
  if (state.selecting) updateSelectBar();
}

// ---------- Seleccionar varios mensajes ----------

// selectable: lo que se puede seleccionar (no los eliminados ni los avisos).
const selectable = (m) => !m.deleted && m.kind !== "event";

// selectedMessages devuelve los seleccionados en el orden de la conversación.
const selectedMessages = () =>
  (state.messages.get(state.current)?.list ?? []).filter((m) => state.selected.has(m.id));

function startSelect(m) {
  closeMsgMenu();
  closeEmojiPanel();
  cancelEdit();
  state.selecting = true;
  state.selected = new Set([m.id]);
  renderMessages({ keepScroll: true });
  syncComposer();
}

function endSelect() {
  if (!state.selecting) return;
  state.selecting = false;
  state.selected.clear();
  renderMessages({ keepScroll: true });
  syncComposer();
}

// toggleSelected marca o desmarca un mensaje; sin ninguno, termina la selección.
function toggleSelected(id) {
  const m = findMessage(id);
  if (!m || !selectable(m)) return;
  if (!state.selected.delete(id)) state.selected.add(id);
  if (state.selected.size === 0) return endSelect();
  document.querySelector(`#messages .msg[data-id="${CSS.escape(id)}"]`)
    ?.classList.toggle("selected", state.selected.has(id));
  updateSelectBar();
}

function updateSelectBar() {
  const msgs = selectedMessages();
  const n = msgs.length;
  $("select-count").textContent = n === 1 ? "1 seleccionado" : `${n} seleccionados`;
  const forward = n > 0 && msgs.every(canForward);
  $("select-forward").disabled = !forward;
  $("select-forward").title = forward ? "" : "Solo se reenvían los mensajes de texto y los archivos enviados o ya descargados";
  const del = n > 0 && msgs.every(canDelete);
  $("select-delete").disabled = !del;
  $("select-delete").title = del ? ""
    : `Solo se eliminan para todos tus propios mensajes de los últimos ${Math.round(state.limits.deleteWindowMs / 60000)} minutos`;
}

async function deleteSelected() {
  const msgs = selectedMessages();
  if (msgs.length === 0) return;
  // El plazo pudo vencer mientras estaban seleccionados.
  if (!msgs.every(canDelete)) {
    updateSelectBar();
    return showBanner($("select-delete").title, 5000);
  }
  const where = msgs[0].roomId ? "todos los miembros de la sala" : memberName(msgs[0].peerId);
  const what = msgs.length === 1 ? "este mensaje" : `estos ${msgs.length} mensajes`;
  const kept = msgs.some((m) => m.transfer?.state === "completed")
    ? " Los archivos que ya se descargaron se quedan en su carpeta." : "";
  if (!confirm(`¿Eliminar ${what} para todos? También se borrarán en el equipo de ${where}.${kept}`)) return;
  const errors = [];
  for (const m of msgs) {
    try {
      addMessage(await api.deleteMessage(m.id));
    } catch (e) {
      errors.push(e.message);
    }
  }
  endSelect();
  if (errors.length) {
    showBanner(`${errors.length === 1 ? "Uno no se pudo eliminar" : `${errors.length} no se pudieron eliminar`}: ${errors[0]}`, 6000);
  }
}

// ---------- Reenviar ----------

// forwardIds: los mensajes que se van a reenviar (del diálogo abierto).
let forwardIds = [];

function openForward(ids) {
  const msgs = ids.map(findMessage).filter((m) => m && canForward(m));
  if (msgs.length === 0) return;
  forwardIds = msgs.map((m) => m.id);
  const withFiles = msgs.some((m) => m.kind === "files");
  $("forward-title").textContent = msgs.length === 1 ? "Reenviar mensaje" : `Reenviar ${msgs.length} mensajes`;
  $("forward-what").textContent = forwardSummary(msgs);
  const rooms = sortedRooms().filter((r) => !r.left).map((r) => roomPickItem(r, withFiles));
  $("forward-list").replaceChildren(...rooms, ...sortedContacts().map(pickItem));
  $("forward-help").textContent = withFiles
    ? "Los archivos se ofrecen otra vez y cada quien decide si los acepta. A las salas no se envían archivos."
    : "Cada uno lo recibe marcado como reenviado. Los desconectados lo reciben al conectarse.";
  $("forward-search").value = "";
  $("forward-error").hidden = true;
  $("forward-send").disabled = false;
  $("forward-dialog").showModal();
  if (!state.mobile) $("forward-search").focus();
}

// forwardSummary resume lo que se reenvía: el inicio de cada texto o los
// nombres de los archivos.
function forwardSummary(msgs) {
  const cut = (s, n) => (s.length > n ? `${s.slice(0, n)}…` : s);
  const lines = msgs.slice(0, 3).map((m) => {
    if (m.kind !== "files") return cut(m.body.replace(/\s+/g, " "), 80);
    const files = m.transfer?.files ?? [];
    return `📎 ${cut(files.slice(0, 3).map((f) => f.name).join(", "), 80)}${files.length > 3 ? ` y ${files.length - 3} más` : ""}`;
  });
  if (msgs.length > 3) lines.push(`… y ${msgs.length - 3} más`);
  return lines.join("\n");
}

// roomPickItem es una casilla para elegir una sala; con archivos no se puede.
function roomPickItem(r, withFiles) {
  const li = document.createElement("li");
  const label = document.createElement("label");
  label.className = "check";
  const box = document.createElement("input");
  box.type = "checkbox";
  box.value = r.id;
  box.dataset.room = "true";
  const icon = document.createElement("span");
  icon.className = "room-icon";
  icon.textContent = "👥";
  const name = document.createElement("span");
  name.textContent = r.name;
  const detail = document.createElement("small");
  detail.className = "help-inline";
  detail.textContent = roomDetail(r);
  if (withFiles) {
    box.disabled = true;
    detail.append(" · no recibe archivos");
    label.title = "A las salas no se pueden enviar archivos";
  }
  label.append(box, icon, name, detail);
  li.append(label);
  return li;
}

function filterForward() {
  const q = $("forward-search").value.trim().toLocaleLowerCase("es");
  for (const li of $("forward-list").children) {
    li.hidden = q !== "" && !li.textContent.toLocaleLowerCase("es").includes(q);
  }
}

async function sendForward(ev) {
  if (ev.submitter?.value !== "save") return;
  ev.preventDefault();
  const checked = [...$("forward-list").querySelectorAll("input:checked")];
  const peers = checked.filter((b) => !b.dataset.room).map((b) => b.value);
  const rooms = checked.filter((b) => b.dataset.room).map((b) => b.value);
  const err = $("forward-error");
  err.hidden = true;
  if (checked.length === 0) {
    err.textContent = "Elige al menos un contacto o una sala.";
    err.hidden = false;
    return;
  }
  $("forward-send").disabled = true;
  try {
    const res = await api.forward(forwardIds, peers, rooms);
    for (const m of res.messages) addMessage(m);
    endSelect();
    if (res.error) {
      // Lo que sí se envió no se repite: el botón queda desactivado.
      err.textContent = `Se reenvió solo en parte: ${res.error}`;
      err.hidden = false;
      return;
    }
    $("forward-dialog").close();
    const targets = [...peers, ...rooms.map((r) => ROOM + r)];
    if (targets.length === 1) openChat(targets[0]);
    else showBanner(`Reenviado a ${targets.length} conversaciones.`, 3000);
  } catch (e) {
    err.textContent = e.message;
    err.hidden = false;
    $("forward-send").disabled = false;
  }
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
  $("receipts-input").checked = state.self.readReceipts;
  $("previews-input").checked = state.self.previews;
  $("thumbs-usage").textContent = "";
  $("thumbs-clear-btn").disabled = true;
  api.thumbsUsage().then(renderThumbsUsage).catch(() => {});
  $("about").textContent = [`LanChat ${s.version} · ${s.hostname}`, s.copyright, s.license].join("\n");
  $("settings-error").hidden = true;
  renderAndroid();
  $("settings").showModal();
  api.system().then(renderSystem).catch(() => {});
}

// askAutostart pregunta, la primera vez (o si el programa cambió de carpeta),
// si LanChat debe iniciar con Windows: nunca se activa sin que el usuario lo
// elija. Cerrar el diálogo con Esc cuenta como "Ahora no".
async function askAutostart() {
  const sys = await api.system();
  if (!sys.askAutostart) return;
  const dialog = $("autostart-dialog");
  $("autostart-moved").hidden = !sys.autostartMoved;
  dialog.returnValue = "";
  dialog.addEventListener("close", () => {
    api.autostart(dialog.returnValue === "yes").catch((e) => showBanner(e.message, 5000));
  }, { once: true });
  dialog.showModal();
}

function renderSystem(sys) {
  $("system-section").hidden = !sys.supported;
  $("uninstall-row").hidden = !sys.supported;
  $("autostart-input").checked = sys.autostart;
  $("firewall-status").textContent = sys.firewall ? "Permitido ✓" : "Sin configurar";
  $("firewall-btn").hidden = sys.firewall;
}

// renderAndroid muestra si Android puede pausar LanChat. LanChatAndroid lo
// pone la app de Android (MainActivity.Bridge); en el escritorio no existe.
function renderAndroid() {
  const android = window.LanChatAndroid;
  $("android-section").hidden = !android;
  if (!android) return;
  $("android-autostart").checked = android.autostart();
  $("android-downloads").textContent = friendlyDir(state.downloadDir);
  const restricted = android.backgroundRestricted();
  $("battery-status").textContent = restricted ? "Android puede pausarlo para ahorrar batería" : "Sin restricción ✓";
  $("battery-btn").hidden = !restricted;
}

function settingsError(e) {
  $("settings-error").textContent = e.message;
  $("settings-error").hidden = false;
}

async function toggleReceipts() {
  try {
    state.self = await api.readReceipts($("receipts-input").checked);
  } catch (e) {
    settingsError(e);
  }
}

async function togglePreviews() {
  const input = $("previews-input");
  try {
    state.self = await api.previews(input.checked);
    renderMessages({ keepScroll: true });
  } catch (e) {
    input.checked = state.self.previews;
    settingsError(e);
  }
}

function renderThumbsUsage(u) {
  $("thumbs-usage").textContent = u.count === 0
    ? "No hay miniaturas guardadas"
    : `${u.count} ${u.count === 1 ? "miniatura guardada" : "miniaturas guardadas"} (${fmtSize(u.bytes)})`;
  $("thumbs-clear-btn").disabled = u.count === 0;
}

// clearThumbs borra las miniaturas ya guardadas; las tarjetas cargadas dejan
// de mostrarlas sin esperar a recargar la conversación.
async function clearThumbs() {
  if (!confirm("¿Borrar todas las miniaturas guardadas? Las imágenes no se borran, pero sus vistas previas no se pueden recuperar.")) return;
  const btn = $("thumbs-clear-btn");
  btn.disabled = true; // compactar la base puede tardar
  try {
    renderThumbsUsage(await api.clearThumbs());
    for (const conv of state.messages.values()) {
      for (const m of conv.list) m.transfer?.files.forEach((f) => (f.thumb = false));
    }
    renderMessages({ keepScroll: true });
  } catch (e) {
    btn.disabled = false;
    settingsError(e);
  }
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

// ---------- Acerca de ----------

function openAbout() {
  const s = state.self;
  $("about-name").textContent = s.appName;
  $("about-version").textContent = `Versión ${s.version}`;
  $("about-license").textContent = s.licenseName;
  $("about-repo").replaceChildren(...breakAfterSlashes(s.repository));
  $("about-copyright").textContent = s.copyright;
  $("about-dialog").showModal();
}

// breakAfterSlashes permite partir una URL después de cada "/" (<wbr>), para
// que en pantallas angostas no se corte a media palabra ni haya que deslizar.
function breakAfterSlashes(url) {
  return url.split(/(?<=\/)/).flatMap((part) => [part, document.createElement("wbr")]);
}

// ---------- Mensaje a varios ----------

function openMany() {
  $("many-list").replaceChildren(...sortedContacts().map(pickItem));
  const groups = $("many-groups");
  groups.replaceChildren();
  for (const name of groupNames()) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "text-btn";
    b.textContent = name;
    b.addEventListener("click", () => selectMany("group", name));
    groups.append(b);
  }
  $("many-error").hidden = true;
  $("many-dialog").showModal();
  $("many-text").focus();
}

// pickItem es una casilla para elegir un contacto en una lista.
function pickItem(c) {
  const li = document.createElement("li");
  const label = document.createElement("label");
  label.className = "check";
  const box = document.createElement("input");
  box.type = "checkbox";
  box.value = c.id;
  box.dataset.online = c.online;
  box.dataset.group = c.group;
  const dot = document.createElement("span");
  dot.className = dotClass(c.online, c.status);
  const name = document.createElement("span");
  name.textContent = c.displayName;
  const detail = document.createElement("small");
  detail.className = "help-inline";
  detail.textContent = c.detail;
  // A un contacto incompatible no se le puede enviar ni agregar a una sala.
  if (c.incompatible) {
    box.disabled = true;
    detail.append(" · ", incompatibleBadge());
    label.title = incompatibleText(c);
  }
  label.append(box, dot, name, detail);
  li.append(label);
  return li;
}

function selectMany(which, group) {
  for (const box of $("many-list").querySelectorAll("input")) {
    box.checked = !box.disabled && (which === "all" ||
      (which === "online" && box.dataset.online === "true") ||
      (which === "group" && box.dataset.group === group));
  }
}

async function sendMany(ev) {
  if (ev.submitter?.value !== "save") return;
  ev.preventDefault();
  const peers = [...$("many-list").querySelectorAll("input:checked")].map((b) => b.value);
  const body = $("many-text").value;
  const err = $("many-error");
  err.hidden = true;
  if (peers.length === 0) {
    err.textContent = "Elige al menos un contacto.";
    err.hidden = false;
    return;
  }
  try {
    const res = await api.sendMany(peers, body);
    for (const m of res.messages) addMessage(m);
    if (res.error) {
      err.textContent = `Se envió a ${res.messages.length}, pero falló con otros: ${res.error}`;
      err.hidden = false;
      return;
    }
    $("many-text").value = "";
    $("many-dialog").close();
    showBanner(`Mensaje enviado a ${res.messages.length} ${res.messages.length === 1 ? "contacto" : "contactos"}.`, 3000);
  } catch (e) {
    err.textContent = e.message;
    err.hidden = false;
  }
}

function openIdentity() {
  const c = state.contacts.get(state.current);
  if (!c) return;
  $("identity-who").textContent = c.displayName;
  $("identity-theirs").textContent = c.fingerprint || "(todavía no se conoce)";
  $("identity-new-box").hidden = !c.identityChanged;
  $("identity-new").textContent = c.newFingerprint;
  $("identity-mine").textContent = state.self.fingerprint;
  $("identity-dialog").showModal();
}

async function trustIdentity() {
  const c = state.contacts.get(state.current);
  if (!c) return;
  if (!confirm(`¿Confiar en la nueva identidad de ${c.displayName}? Hazlo solo si confirmaste con esa persona que reinstaló LanChat (puedes comparar las huellas en "Identidad").`)) return;
  try {
    await request("POST", "/api/trust", { peer: c.id });
  } catch (e) {
    showBanner(e.message, 5000);
  }
}

function openGroup() {
  const c = state.contacts.get(state.current);
  if (!c) return;
  $("group-who").textContent = c.displayName;
  $("group-input").value = c.group;
  $("group-input").maxLength = state.limits.maxName;
  const list = $("group-names");
  list.replaceChildren(...groupNames().map((n) => Object.assign(document.createElement("option"), { value: n })));
  $("group-error").hidden = true;
  $("group-dialog").showModal();
}

async function saveGroup(ev) {
  if (ev.submitter?.value !== "save") return;
  ev.preventDefault();
  try {
    await api.group(state.current, $("group-input").value);
    $("group-dialog").close();
  } catch (e) {
    $("group-error").textContent = e.message;
    $("group-error").hidden = false;
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

// ---------- Salas ----------

// MAX_ROOM_MEMBERS coincide con protocol.MaxRoomMembers.
const MAX_ROOM_MEMBERS = 50;

// openRoomDialog sirve para crear una sala ("create"), agregar miembros
// ("add") o renombrarla ("rename").
let roomMode = "create";
function openRoomDialog(mode) {
  roomMode = mode;
  const r = currentRoom();
  const titles = { create: "Nueva sala", add: `Agregar a «${r?.name}»`, rename: "Renombrar la sala" };
  $("room-title").textContent = titles[mode];
  $("room-save").textContent = { create: "Crear", add: "Agregar", rename: "Guardar" }[mode];
  $("room-name-box").hidden = mode === "add";
  $("room-members-box").hidden = mode === "rename";
  $("room-name").value = mode === "rename" ? r.name : "";
  $("room-name").maxLength = state.limits.maxName;
  const members = new Set(mode === "add" ? r.members : []);
  const candidates = sortedContacts().filter((c) => !members.has(c.id));
  $("room-list").replaceChildren(...candidates.map(pickItem));
  $("room-help").textContent = mode === "create"
    ? `Quedas como administrador: puedes sacar miembros y nombrar más administradores. Todos pueden escribir, agregar a otros y cambiar el nombre. Hasta ${MAX_ROOM_MEMBERS} miembros.`
    : "Verán los mensajes a partir de ahora, no los anteriores.";
  $("room-error").hidden = true;
  $("room-dialog").showModal();
  (mode === "add" ? $("room-list").querySelector("input") : $("room-name"))?.focus();
}

async function saveRoom(ev) {
  if (ev.submitter?.value !== "save") return;
  ev.preventDefault();
  const picked = [...$("room-list").querySelectorAll("input:checked")].map((b) => b.value);
  const name = $("room-name").value;
  try {
    if (roomMode === "create") {
      const r = await api.createRoom(name, picked);
      state.rooms.set(r.id, r);
      $("room-dialog").close();
      openChat(ROOM + r.id);
      return;
    }
    const id = roomId(state.current);
    if (roomMode === "add") await api.room("members", id, { members: picked });
    else await api.room("rename", id, { name });
    $("room-dialog").close();
  } catch (e) {
    $("room-error").textContent = e.message;
    $("room-error").hidden = false;
  }
}

// openMembers muestra los miembros de la sala; a un administrador le deja
// sacar a otros y nombrar o quitar administradores.
function openMembers() {
  if (!currentRoom()) return;
  $("members-error").hidden = true;
  renderMembers();
  $("members-dialog").showModal();
}

function renderMembers() {
  const r = currentRoom();
  if (!r) return $("members-dialog").close();
  const me = state.self.id;
  const iAdmin = !r.left && r.admins.includes(me);
  $("members-title").textContent = `Miembros de «${r.name}» (${r.members.length})`;
  $("members-list").replaceChildren(...r.members.map((id) => {
    const li = document.createElement("li");
    const name = document.createElement("span");
    name.className = "name";
    name.textContent = id === me ? `${state.self.name || state.self.hostname} (tú)` : memberName(id);
    li.append(name);
    const admin = r.admins.includes(id);
    if (admin) {
      const tag = document.createElement("span");
      tag.className = "admin-tag";
      tag.textContent = "Administrador";
      li.append(tag);
    }
    if (iAdmin && id !== me) {
      li.append(textButton(admin ? "Quitar admin" : "Hacer admin", () =>
        memberAction("admin", r.id, { member: id, admin: !admin })));
      const kick = textButton("Sacar", () => {
        if (confirm(`¿Sacar a ${memberName(id)} de «${r.name}»? Dejará de recibir los mensajes de la sala.`)) {
          memberAction("kick", r.id, { member: id });
        }
      });
      kick.classList.add("danger-btn");
      li.append(kick);
    }
    return li;
  }));
  $("members-help").textContent = r.left
    ? "Ya no estás en esta sala: esta es la lista de cuando saliste."
    : iAdmin
      ? "Como administrador puedes sacar a otros miembros y nombrar o quitar administradores."
      : "Solo los administradores pueden sacar a otros miembros o nombrar administradores.";
  $("members-add").hidden = r.left;
  // Sin nadie más, el administrador puede eliminar la sala.
  $("members-destroy").hidden = !(iAdmin && r.members.length === 1);
}

async function memberAction(action, room, extra) {
  $("members-error").hidden = true;
  try {
    await api.room(action, room, extra); // la lista se actualiza con el evento "room"
  } catch (e) {
    $("members-error").textContent = e.message;
    $("members-error").hidden = false;
  }
}

async function destroyRoom() {
  const r = currentRoom();
  if (!r || !confirm(`¿Eliminar la sala «${r.name}»? Se borrará de este equipo con todos sus mensajes.`)) return;
  $("members-dialog").close();
  try {
    await api.room("destroy", r.id); // el evento "cleared" la quita de la lista
  } catch (e) {
    showBanner(e.message, 5000);
  }
}

async function leaveRoom() {
  const r = currentRoom();
  if (!r || !confirm(`¿Salir de «${r.name}»? Conservarás el historial, pero ya no recibirás sus mensajes.`)) return;
  try {
    await api.room("leave", r.id);
  } catch (e) {
    showBanner(e.message, 5000);
  }
}

// ---------- Privacidad ----------

const WIPE_WORD = "BORRAR";

// exportData descarga la copia de la base (el navegador la guarda en Descargas).
function exportData() {
  if (state.mobile) return saveExport();
  const a = document.createElement("a");
  a.href = "/api/data/export";
  a.download = "";
  document.body.append(a);
  a.click();
  a.remove();
  showBanner("Descargando la copia de tus datos…", 4000);
}

// saveExport: sin escritorio (la WebView no descarga), el núcleo guarda la
// copia directamente en la carpeta de archivos recibidos.
async function saveExport() {
  showBanner("Guardando la copia de tus datos…");
  try {
    const { name, dir } = await request("POST", "/api/data/export", {});
    showBanner(`Copia guardada en ${friendlyDir(dir)} › ${name}`, 8000);
  } catch (e) {
    showBanner(e.message, 6000);
  }
}

// friendlyDir muestra la carpeta como el explorador de archivos de Android.
function friendlyDir(dir) {
  return dir.endsWith("/Download/LanChat") ? "Descargas › LanChat" : dir;
}

async function deleteChat() {
  const room = currentRoom();
  const c = state.contacts.get(state.current);
  let question;
  if (room) {
    question = room.left
      ? `¿Borrar la sala «${room.name}» y todos sus mensajes de este equipo?`
      : `¿Borrar todos los mensajes de «${room.name}» de este equipo? Sigues en la sala; los demás conservan sus mensajes.`;
  } else if (c) {
    question = `¿Borrar toda la conversación con ${c.displayName} de este equipo? Se cancelan los archivos pendientes con este contacto. ${c.displayName} conserva su copia.`;
  } else {
    return;
  }
  if (!confirm(question)) return;
  try {
    if (room) await api.room("delete", room.id);
    else await api.deleteConversation(c.id);
  } catch (e) {
    showBanner(e.message, 5000);
  }
}

// ---------- Quitar contacto ----------

// openRemove pregunta qué hacer con la conversación del contacto (desconectado)
// antes de quitarlo: combinarla con la de otro contacto, que suele ser la
// misma persona con una identidad nueva, o borrarla.
function openRemove() {
  const c = state.contacts.get(state.current);
  if (!c || c.online) return;
  $("remove-who").textContent = c.displayName;
  const candidates = mergeCandidates(c);
  const select = $("remove-into");
  select.replaceChildren(...candidates.map((o) => Object.assign(document.createElement("option"), {
    value: o.id,
    textContent: `${o.displayName} · ${o.detail}${o.online ? "" : " (desconectado)"}`,
  })));
  const conv = state.messages.get(c.id);
  const hasMessages = c.unread > 0 || !conv || conv.list.length > 0;
  $("remove-choice").hidden = !hasMessages;
  const merge = candidates.length > 0 && samePerson(c, candidates[0]);
  $("remove-form").elements["remove-mode"].value = merge ? "merge" : "delete";
  for (const el of $("remove-form").querySelectorAll('input[value="merge"], #remove-into')) {
    el.disabled = candidates.length === 0;
  }
  $("remove-error").hidden = true;
  $("remove-dialog").showModal();
}

// samePerson: mismo nombre propio o mismo nombre de equipo.
function samePerson(a, b) {
  const same = (x, y) => Boolean(x) && x.toLocaleLowerCase("es") === y.toLocaleLowerCase("es");
  return same(a.name, b.name) || same(a.hostname, b.hostname);
}

// mergeCandidates: los demás contactos, primero los que parecen la misma
// persona (y de ellos, los conectados).
function mergeCandidates(c) {
  const rank = (o) => (samePerson(c, o) ? 0 : 2) + (o.online ? 0 : 1);
  return sortedContacts().filter((o) => o.id !== c.id).sort((a, b) => rank(a) - rank(b));
}

async function removeContact(ev) {
  if (ev.submitter?.value !== "save") return;
  ev.preventDefault();
  const c = state.contacts.get(state.current);
  if (!c) return $("remove-dialog").close();
  const merge = !$("remove-choice").hidden && $("remove-form").elements["remove-mode"].value === "merge";
  const into = merge ? $("remove-into").value : "";
  if (!$("remove-choice").hidden && !merge &&
      !confirm(`¿Borrar toda la conversación con ${c.displayName} de este equipo?`)) return;
  try {
    await api.removeContact(c.id, into);
    $("remove-dialog").close();
  } catch (e) {
    $("remove-error").textContent = e.message;
    $("remove-error").hidden = false;
  }
}

// contactRemoved llega cuando se quitó un contacto (en esta u otra ventana).
// Si su conversación se combinó con la de otro, se abre esa.
function contactRemoved(ev) {
  state.messages.delete(ev.peer);
  if (ev.into) state.messages.delete(ev.into);
  if (state.current === ev.peer) {
    if (ev.into && state.contacts.has(ev.into)) openChat(ev.into);
    else showEmpty();
  } else if (ev.into && state.current === ev.into) {
    loadHistory(ev.into);
  } else {
    renderContacts();
  }
}

// messageDeleted llega cuando se borró de este equipo una oferta de archivos
// (en esta u otra ventana): se quita de su conversación.
function messageDeleted(ev) {
  state.progress.delete(ev.id);
  const conv = state.messages.get(ev.peer);
  if (!conv) return;
  conv.list = conv.list.filter((m) => m.id !== ev.id);
  if (ev.peer === state.current) renderMessages({ keepScroll: true });
}

// cleared llega cuando se borró una conversación (en esta u otra ventana).
function cleared(ev) {
  const key = ev.room ? ROOM + ev.room : ev.peer;
  if (ev.removed) state.rooms.delete(ev.room);
  if (key !== state.current) {
    state.messages.delete(key);
    renderContacts();
    return;
  }
  if (ev.removed) return showEmpty();
  state.messages.set(key, { list: [], complete: true });
  renderMessages();
}

function openWipe() {
  $("settings").close();
  $("wipe-input").value = "";
  $("wipe-btn").disabled = true;
  $("wipe-downloads").textContent = state.downloadDir || "Descargas\\LanChat";
  $("wipe-error").hidden = true;
  $("wipe-dialog").showModal();
}

async function wipe(ev) {
  if (ev.submitter?.value !== "save") return;
  ev.preventDefault();
  try {
    await api.wipe($("wipe-input").value.trim());
    $("wipe-dialog").close();
  } catch (e) {
    $("wipe-error").textContent = e.message;
    $("wipe-error").hidden = false;
  }
}

// reload llega tras borrar todos los datos: se empieza de cero.
async function reloadAll() {
  state.messages.clear();
  state.progress.clear();
  showEmpty();
  await loadState().catch((e) => showBanner(e.message));
  showBanner("Se borraron todos tus datos de este equipo.", 5000);
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
    if (!m.outgoing && convKey(m) === state.current) markReadIfVisible();
  });

  es.addEventListener("room", (e) => {
    const r = JSON.parse(e.data);
    state.rooms.set(r.id, r);
    renderContacts();
    if (ROOM + r.id === state.current) {
      renderHeader();
      if ($("members-dialog").open) renderMembers();
      if (r.unread > 0) markReadIfVisible();
    }
  });

  es.addEventListener("self", (e) => {
    state.self = JSON.parse(e.data);
    renderSelf();
  });

  es.addEventListener("transfer", (e) => updateTransfer(JSON.parse(e.data)));

  es.addEventListener("progress", (e) => {
    const p = JSON.parse(e.data);
    state.progress.set(p.id, p);
    const card = cardElement(p.id);
    if (card) paintProgress(card, p);
  });

  es.addEventListener("cleared", (e) => cleared(JSON.parse(e.data)));
  es.addEventListener("deleted", (e) => messageDeleted(JSON.parse(e.data)));
  es.addEventListener("removed", (e) => contactRemoved(JSON.parse(e.data)));
  es.addEventListener("reload", reloadAll);

  es.addEventListener("focus", () => window.focus());

  // El desinstalador cerró LanChat: la ventana se cierra para soltar su perfil.
  es.addEventListener("quit", () => {
    es.close();
    window.close();
    showBanner("LanChat se cerró. Puedes cerrar esta ventana.");
  });
}

// ---------- Inicio ----------

function bind() {
  $("search").addEventListener("input", renderContacts);
  $("offline-btn").addEventListener("click", () => setHideOffline(!hideOffline()));
  $("settings-btn").addEventListener("click", openSettings);
  $("status-btn").addEventListener("click", openStatus);
  $("many-btn").addEventListener("click", openMany);
  $("new-room-btn").addEventListener("click", () => openRoomDialog("create"));
  $("emoji-btn").addEventListener("click", () =>
    ($("emoji-panel").hidden ? openEmojiPanel() : closeEmojiPanel()));
  // El selector se cierra con Esc o al hacer clic fuera de él.
  // composedPath y no contains: al cambiar de pestaña el botón pulsado se
  // redibuja y ya no está dentro del panel cuando el clic llega aquí.
  document.addEventListener("click", (e) => {
    const path = e.composedPath();
    if (!$("emoji-panel").hidden && !path.includes($("emoji-panel")) && !path.includes($("emoji-btn"))) {
      closeEmojiPanel();
    }
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") closeEmojiPanel();
  });
  $("room-members-btn").addEventListener("click", openMembers);
  $("members-add").addEventListener("click", () => {
    $("members-dialog").close();
    openRoomDialog("add");
  });
  $("members-destroy").addEventListener("click", destroyRoom);
  // El menú de un mensaje se cierra al hacer clic fuera, con Esc o al desplazar.
  document.addEventListener("click", (e) => {
    if (!$("msg-menu").hidden && !$("msg-menu").contains(e.target)) closeMsgMenu();
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") closeMsgMenu();
  });
  $("messages").addEventListener("scroll", closeMsgMenu, { passive: true });
  $("room-rename-btn").addEventListener("click", () => openRoomDialog("rename"));
  $("room-leave-btn").addEventListener("click", leaveRoom);
  $("room-form").addEventListener("submit", saveRoom);
  $("delete-chat-btn").addEventListener("click", deleteChat);
  $("remove-contact-btn").addEventListener("click", openRemove);
  $("remove-form").addEventListener("submit", removeContact);
  $("share-form").addEventListener("submit", sendShare);
  // Cancelar, Atrás o terminar de enviar: la app deja de servir los archivos.
  $("share-dialog").addEventListener("close", shareFinished);
  for (const b of document.querySelectorAll("[data-share-select]")) {
    b.addEventListener("click", () => selectShare(b.dataset.shareSelect));
  }
  $("export-btn").addEventListener("click", exportData);
  $("wipe-export-btn").addEventListener("click", exportData);
  $("wipe-open-btn").addEventListener("click", openWipe);
  $("wipe-form").addEventListener("submit", wipe);
  $("wipe-input").addEventListener("input", () =>
    ($("wipe-btn").disabled = $("wipe-input").value.trim() !== WIPE_WORD));
  $("about-btn").addEventListener("click", openAbout);
  $("settings-about").addEventListener("click", () => {
    $("settings").close();
    openAbout();
  });
  $("about-repo").addEventListener("click", () =>
    request("POST", "/api/open-repository", {}).catch((e) => showBanner(e.message, 5000)));
  $("many-form").addEventListener("submit", sendMany);
  for (const b of document.querySelectorAll("[data-select]")) {
    b.addEventListener("click", () => selectMany(b.dataset.select));
  }
  $("status-form").addEventListener("submit", saveStatus);
  $("settings-form").addEventListener("submit", saveSettings);
  $("alias-btn").addEventListener("click", openAlias);
  $("group-btn").addEventListener("click", openGroup);
  $("identity-btn").addEventListener("click", openIdentity);
  $("trust-btn").addEventListener("click", trustIdentity);
  $("group-form").addEventListener("submit", saveGroup);
  $("alias-form").addEventListener("submit", saveAlias);
  $("back-btn").addEventListener("click", closeChat);
  // Visor de imágenes: tocar en cualquier parte lo cierra; al cerrar se suelta
  // la imagen (puede ser grande).
  $("viewer").addEventListener("click", () => $("viewer").close());
  $("viewer").addEventListener("close", () => $("viewer-img").removeAttribute("src"));
  $("more-btn").addEventListener("click", (e) => {
    e.stopPropagation(); // que no lo cierre el clic en el documento
    setMenu(!$("chat-actions").classList.contains("open"));
  });
  // Elegir una acción, tocar fuera o Escape cierran el menú.
  $("chat-actions").addEventListener("click", () => setMenu(false));
  document.addEventListener("click", (e) => {
    if (!$("chat-actions").contains(e.target)) setMenu(false);
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") setMenu(false);
  });

  $("composer").addEventListener("submit", (e) => {
    e.preventDefault();
    sendMessage();
  });
  $("input").addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      sendMessage();
    } else if (e.key === "Escape" && state.editing) {
      cancelEdit();
    }
  });
  $("edit-cancel").addEventListener("click", cancelEdit);
  // Al seleccionar, tocar un mensaje lo marca o desmarca (y no hace nada más:
  // ni abrir archivos ni aceptar envíos).
  $("messages").addEventListener("click", (e) => {
    if (!state.selecting) return;
    e.preventDefault();
    e.stopPropagation();
    const el = e.target.closest(".msg[data-id]");
    if (el) toggleSelected(el.dataset.id);
  }, true);
  $("select-cancel").addEventListener("click", endSelect);
  $("select-delete").addEventListener("click", deleteSelected);
  $("select-forward").addEventListener("click", () => openForward(selectedMessages().map((m) => m.id)));
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && state.selecting && !document.querySelector("dialog[open]")) endSelect();
  });
  $("forward-form").addEventListener("submit", sendForward);
  $("forward-search").addEventListener("input", filterForward);
  $("input").addEventListener("input", autoGrow);
  $("attach-btn").addEventListener("click", (e) =>
    state.mobile ? $("file-input").click() : pickAndSend(e.currentTarget, api.pickFiles));
  $("file-input").addEventListener("change", (e) => uploadPicked(e.currentTarget));
  $("folder-btn").addEventListener("click", (e) => pickAndSend(e.currentTarget, api.pickFolder));
  $("autostart-input").addEventListener("change", toggleAutostart);
  // El desinstalador (otro proceso) pregunta antes de borrar nada.
  $("uninstall-btn").addEventListener("click", () => {
    $("settings").close();
    api.uninstall().catch((e) => showBanner(e.message, 5000));
  });
  $("receipts-input").addEventListener("change", toggleReceipts);
  $("previews-input").addEventListener("change", togglePreviews);
  $("thumbs-clear-btn").addEventListener("click", clearThumbs);
  $("firewall-btn").addEventListener("click", allowFirewall);
  $("battery-btn").addEventListener("click", () => window.LanChatAndroid?.allowBackground());
  $("app-settings-btn").addEventListener("click", () => window.LanChatAndroid?.openAppSettings());
  $("stop-btn").addEventListener("click", () => window.LanChatAndroid?.stop());
  $("android-autostart").addEventListener("change", (e) =>
    window.LanChatAndroid?.setAutostart(e.currentTarget.checked));
  $("open-downloads").addEventListener("click", () =>
    api.openDownloadDir().catch((e) => showBanner(e.message, 5000)));

  // Arrastrar y soltar archivos sobre la conversación.
  const chat = $("chat");
  let dragDepth = 0;
  const hasFiles = (e) => e.dataTransfer?.types.includes("Files");
  chat.addEventListener("dragenter", (e) => {
    if (!hasFiles(e) || !state.current || roomId(state.current)) return;
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
    if (hasFiles(e) && !roomId(state.current)) uploadDropped(e.dataTransfer);
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

// lanchatResume lo llama la app de Android al volver a primer plano (p. ej.
// desde el diálogo de batería): el estado de Ajustes pudo cambiar.
window.lanchatResume = () => {
  if ($("settings").open) renderAndroid();
};

bind();
loadState()
  .then(connectEvents)
  .then(askAutostart)
  .catch((e) => showBanner(e.message));
