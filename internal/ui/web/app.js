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
  profile: null, // id del contacto cuya ficha está abierta
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
  // replyTo: id del mensaje al que se responde (null = ninguno).
  replyTo: null,
  // selecting: modo de selección de varios mensajes (selected: sus ids).
  selecting: false,
  selected: new Set(),
  // dropTarget: contacto de la lista sobre el que se arrastran archivos.
  dropTarget: null,
};

// Extensiones de programas o scripts: se pide confirmación antes de abrirlos.
const RISKY_EXT = /\.(exe|msi|msix|bat|cmd|com|scr|pif|cpl|ps1|psm1|vbs|vbe|js|jse|wsf|wsh|hta|lnk|reg|jar|dll|appx)$/i;

const encoder = new TextEncoder();

// ---------- Idioma ----------

// Los textos vienen de GET /api/locale (internal/i18n/locales/<idioma>.json):
// el idioma del sistema o el elegido en Ajustes, con las claves que le falten
// tomadas del inglés.
const i18n = { lang: "en", messages: {} };
let pluralRules = new Intl.PluralRules("en");
let timeFmt, dayFmt, weekdayFmt, shortDateFmt, copyDateFmt, expiryFmt;

// t traduce key: params sustituye los {datos} del texto, y params.n elige la
// forma de plural.
function t(key, params = {}) {
  let text = i18n.messages[key];
  if (text == null) return key;
  if (typeof text === "object") text = text[pluralRules.select(params.n ?? 0)] ?? text.other;
  return text.replace(/\{(\w+)\}/g, (all, name) => (name in params ? String(params[name]) : all));
}

async function loadLocale() {
  const res = await fetch("/api/locale");
  if (!res.ok) throw new Error(`/api/locale: ${res.status}`);
  setLocale(await res.json());
}

function setLocale({ lang, messages }) {
  i18n.lang = lang;
  i18n.messages = messages;
  pluralRules = new Intl.PluralRules(lang);
  timeFmt = new Intl.DateTimeFormat(lang, { hour: "2-digit", minute: "2-digit" });
  dayFmt = new Intl.DateTimeFormat(lang, { weekday: "long", day: "numeric", month: "long", year: "numeric" });
  weekdayFmt = new Intl.DateTimeFormat(lang, { weekday: "short" });
  shortDateFmt = new Intl.DateTimeFormat(lang, { day: "2-digit", month: "2-digit", year: "2-digit" });
  copyDateFmt = new Intl.DateTimeFormat(lang, { day: "2-digit", month: "2-digit", year: "numeric" });
  expiryFmt = new Intl.DateTimeFormat(lang, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });
  document.documentElement.lang = lang;
  translatePage(document);
}

// translatePage pone los textos de la página marcados con data-i18n (el
// contenido) y data-i18n-title, -placeholder y -aria-label (esos atributos).
function translatePage(root) {
  for (const el of root.querySelectorAll("[data-i18n]")) el.textContent = t(el.dataset.i18n);
  for (const [attr, key] of [["title", "i18nTitle"], ["placeholder", "i18nPlaceholder"], ["aria-label", "i18nAriaLabel"]]) {
    for (const el of root.querySelectorAll(`[data-i18n-${attr}]`)) el.setAttribute(attr, t(el.dataset[key]));
  }
}

// compareNames ordena nombres como en el idioma de la página.
const compareNames = (a, b) => a.localeCompare(b, i18n.lang, { sensitivity: "base" });

// joinNames une nombres como en el idioma: "Ana, Luis y Eva".
const joinNames = (names) => new Intl.ListFormat(i18n.lang, { type: "conjunction" }).format(names);

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
    throw new ApiError(t("api.noResponse"));
  }
  if (res.status === 401 || res.status === 403) {
    expire();
    throw new ApiError(t("api.expired"));
  }
  if (res.status === 204) return null;
  const data = await res.json().catch(() => null);
  if (!res.ok) throw new ApiError(data?.error ?? t("api.error", { status: res.status }));
  return data;
}

const api = {
  state: () => request("GET", "/api/state"),
  history: (peer, before) =>
    request("GET", `/api/messages?${new URLSearchParams({ peer, before: before ?? "" })}`),
  send: (peer, body) => request("POST", "/api/messages", { peer, body }),
  read: (peer) => request("POST", "/api/read", { peer }),
  name: (name) => request("POST", "/api/name", { name }),
  photo: (file) => {
    const form = new FormData();
    form.append("photo", file);
    return request("POST", "/api/photo", form);
  },
  removePhoto: () => request("POST", "/api/photo/remove", {}),
  status: (status, text, autoAway) => request("POST", "/api/status", { status, text, autoAway }),
  sendMany: (peers, body) => request("POST", "/api/messages/many", { peers, body }),
  deleteMessage: (id) => request("POST", "/api/messages/delete", { id }),
  react: (id, emoji) => request("POST", "/api/messages/react", { id, emoji }),
  editMessage: (id, body) => request("POST", "/api/messages/edit", { id, body }),
  reply: (id, body) => request("POST", "/api/messages/reply", { id, body }),
  forward: (ids, peers, rooms) => request("POST", "/api/messages/forward", { ids, peers, rooms }),
  readReceipts: (enabled) => request("POST", "/api/read-receipts", { enabled }),
  language: (language) => request("POST", "/api/language", { language }),
  previews: (enabled) => request("POST", "/api/previews", { enabled }),
  hideOffline: (hide) => request("POST", "/api/hide-offline", { hide }),
  collapsedGroups: (list) => request("POST", "/api/collapsed-groups", { list }),
  recentEmoji: (list) => request("POST", "/api/recent-emoji", { list }),
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
  saveAs: (id) => request("POST", "/api/transfers/save-as", { id }),
  openFile: (id, index, reveal) => request("POST", "/api/files/open", { id, index, reveal }),
  downloadDir: (dir) => request("POST", "/api/download-dir", { dir }),
  openDownloadDir: () => request("POST", "/api/download-dir/open", {}),
  system: () => request("GET", "/api/system"),
  autostart: (enabled) => request("POST", "/api/system/autostart", { enabled }),
  firewall: () => request("POST", "/api/system/firewall", {}),
  uninstall: () => request("POST", "/api/system/uninstall", {}),
  updateCheck: () => request("POST", "/api/update/check", {}),
  updateInstall: () => request("POST", "/api/update/install", {}),
  updateLater: () => request("POST", "/api/update/later", {}),
  autoUpdate: (enabled) => request("POST", "/api/update/auto", { enabled }),
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
  showBanner(t("api.oldSession"));
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
  state.update = s.update;
  state.languages = s.languages;
  document.body.classList.toggle("mobile", s.mobile);
  if (s.mobile) {
    $("input").placeholder = t("composer.placeholderMobile");
    $("autoaway-text").textContent = t("status.autoAwayMobile");
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

// sortedContacts: los de la lista, como en WhatsApp: arriba quien escribió o
// recibió algo más recientemente; los que no tienen mensajes, después (primero
// los conectados, luego por nombre). Los quitados siguen en state.contacts
// solo para nombrarlos en las salas.
function sortedContacts() {
  return [...state.contacts.values()].filter((c) => !c.removed).sort(
    (a, b) => lastAt(b) - lastAt(a) || b.online - a.online ||
      compareNames(a.displayName, b.displayName),
  );
}

// lastAt es la hora del último mensaje de un contacto o sala (0 si no hay).
const lastAt = (conv) => conv.last?.at ?? 0;

// touchConversation actualiza el último mensaje de la conversación de m: un
// mensaje nuevo la sube al principio de la lista y cambia su vista previa;
// un evento del mismo mensaje (entrega, lectura, edición, eliminación) solo
// la vista previa. Uno de un mensaje anterior no cambia nada.
function touchConversation(m) {
  const conv = m.roomId ? state.rooms.get(m.roomId) : state.contacts.get(m.peerId);
  if (!conv) return;
  const last = conv.last;
  if (last && m.id !== last.id && m.at <= last.at) return;
  const merged = last?.id === m.id ? { ...last, ...m } : { ...m };
  // Un evento atrasado (p. ej. la entrega) no revive un mensaje eliminado
  // ni hace retroceder las marcas.
  if (last?.id === m.id) {
    merged.deleted ||= last.deleted;
    merged.readAt ||= last.readAt;
    if (last.status === "delivered") merged.status = "delivered";
  }
  conv.last = merged;
  renderContacts();
}

// listTime es la hora del último mensaje en la lista, como en WhatsApp: la
// hora si es de hoy, "Ayer", el día de la semana si es de esta semana, o la fecha.
function listTime(ts) {
  const d = new Date(ts);
  const now = new Date();
  if (d.toDateString() === now.toDateString()) return timeFmt.format(d);
  const days = Math.round((new Date(now.toDateString()) - new Date(d.toDateString())) / 86400000);
  if (days === 1) return t("time.yesterday");
  if (days > 1 && days < 7) return weekdayFmt.format(d);
  return shortDateFmt.format(d);
}

// previewText es el comienzo del último mensaje, para la lista.
function previewText(m) {
  if (m.deleted) return "🚫 " + t(m.outgoing ? "msg.youDeleted" : "msg.deleted");
  if (m.kind === "event") return noticeText(m);
  if (!m.roomId) return m.body;
  return t("list.author", { name: m.outgoing ? t("msg.you") : memberName(m.peerId).split(" ")[0], text: m.body });
}

// previewElement es la segunda línea de un contacto o sala con mensajes: las
// marcas de entrega si el último es tuyo y su comienzo.
function previewElement(m) {
  const span = document.createElement("span");
  span.className = "detail last-msg" + (m.deleted || m.kind === "event" ? " muted-preview" : "");
  if (m.outgoing && !m.deleted && m.kind !== "event") {
    const ticks = document.createElement("span");
    ticks.className = "ticks" + (m.readAt ? " read" : "");
    ticks.textContent = m.status === "pending" ? "🕓 " : m.readAt ? "✓✓ " : "✓ ";
    ticks.title = t(m.status === "pending" ? "ticks.pending" : m.readAt ? "ticks.read" : "ticks.delivered");
    span.append(ticks);
  }
  span.append(previewText(m));
  return span;
}

// nameRow es la primera línea de la lista: el nombre y, a la derecha, la
// hora del último mensaje (resaltada si hay mensajes sin leer).
function nameRow(name, conv) {
  const row = document.createElement("span");
  row.className = "name-row";
  row.append(name);
  if (conv.last) {
    const time = document.createElement("span");
    time.className = "time" + (conv.unread > 0 ? " unread" : "");
    time.textContent = listTime(conv.last.at);
    time.title = `${dayLabel(conv.last.at)} ${timeFmt.format(new Date(conv.last.at))}`;
    row.append(time);
  }
  return row;
}

// unreadBadge es el contador de mensajes sin leer (null si no hay).
function unreadBadge(n) {
  if (n <= 0) return null;
  const badge = document.createElement("span");
  badge.className = "badge";
  badge.textContent = n > 99 ? "99+" : n;
  return badge;
}

// secondRow es la segunda línea: vista previa (o detalle) y el contador.
function secondRow(content, unread) {
  const row = document.createElement("span");
  row.className = "second-row";
  row.append(content);
  const badge = unreadBadge(unread);
  if (badge) row.append(badge);
  return row;
}

function totalUnread() {
  let n = 0;
  for (const c of state.contacts.values()) n += c.unread;
  for (const r of state.rooms.values()) n += r.unread;
  return n;
}

// ---------- Render: barra lateral ----------

const STATUS_KEYS = { available: "status.available", away: "status.away", busy: "status.busy" };

// statusLabel describe el estado de un contacto en línea ("" si no lo está).
function statusLabel(status, text) {
  if (!status) return "";
  return t(STATUS_KEYS[status] ?? "status.available") + (text ? ` · ${text}` : "");
}

// statusParts arma la línea de estado del encabezado: la palabra del estado
// en su color (o "Desconectado" en gris) y el mensaje en cursiva.
function statusParts(c) {
  const word = document.createElement("span");
  if (!c.online) {
    word.className = "state-word";
    word.textContent = t("status.offline");
    return [word];
  }
  const status = STATUS_KEYS[c.status] ? c.status : "available";
  word.className = `state-word ${status}`;
  word.textContent = t(STATUS_KEYS[status]);
  if (!c.statusText) return [word];
  const text = document.createElement("span");
  text.className = "status-text";
  text.textContent = c.statusText;
  return [word, " · ", text];
}

function dotClass(online, status) {
  return "dot" + (online ? ` online ${status || "available"}` : "");
}

// ---------- Avatares ----------

// Colores de fondo de los avatares sin foto: con letras blancas se leen en
// los temas claro y oscuro. A cada contacto le toca siempre el mismo.
const AVATAR_COLORS = ["#e11d48", "#db2777", "#c026d3", "#7c3aed", "#4f46e5", "#2563eb",
  "#0369a1", "#0f766e", "#047857", "#4d7c0f", "#b45309", "#c2410c"];

function avatarColor(key) {
  let h = 0;
  for (const ch of key) h = (h * 31 + ch.codePointAt(0)) >>> 0;
  return AVATAR_COLORS[h % AVATAR_COLORS.length];
}

// initials son las iniciales del nombre: de la primera y la última palabra
// ("María Fernanda Ortiz" → "MO"), o una sola si es una palabra.
function initials(name) {
  const isChar = (ch) => /[\p{L}\p{N}]/u.test(ch);
  const words = name.split(/[\s._-]+/).filter((w) => [...w].some(isChar));
  if (words.length === 0) return "?";
  const first = (w) => [...w].find(isChar);
  return (first(words[0]) + (words.length > 1 ? first(words.at(-1)) : "")).toLocaleUpperCase(i18n.lang);
}

// photoURL es la dirección de la foto de perfil de un equipo (null si no
// tiene). La versión (photo) cambia con la foto, para no ver una vieja en caché.
function photoURL(id, photo) {
  return photo ? `/api/photo?${new URLSearchParams({ id, v: photo })}` : null;
}

// avatarElement es el círculo con la foto o, si no hay, las iniciales (o
// fallback) sobre un color fijo para key.
function avatarElement(key, name, photo, fallback) {
  const a = document.createElement("span");
  a.className = "avatar";
  a.style.setProperty("--avatar-bg", avatarColor(key));
  const letters = document.createElement("span");
  letters.className = "initials";
  letters.textContent = fallback ?? initials(name);
  if (photo) {
    const img = document.createElement("img");
    img.alt = "";
    img.src = photo;
    img.addEventListener("error", () => img.replaceWith(letters));
    a.append(img);
  } else {
    a.append(letters);
  }
  return a;
}

// contactAvatar lleva encima el punto de presencia, como antes en la lista.
function contactAvatar(c) {
  const a = avatarElement(c.id, c.displayName, photoURL(c.id, c.photo));
  const dot = document.createElement("span");
  dot.className = dotClass(c.online, c.status);
  a.append(dot);
  return a;
}

function roomAvatar(r) {
  return avatarElement(r.id, r.name, null, "👥");
}

function renderSelf() {
  const s = state.self;
  $("me-name").textContent = s.name || s.hostname;
  $("me-avatar").replaceChildren(selfAvatar());
  if ($("settings").open) renderPhotoSettings();
  $("me-detail").textContent = s.name ? s.hostname : t("self.noName");
  // Inactivo y Disponible: los demás te ven Ausente.
  const shown = s.status === "available" && s.idle ? "away" : s.status;
  $("status-dot").className = dotClass(true, shown);
  $("status-label").textContent = statusLabel(shown, s.statusText) + (shown !== s.status ? ` (${t("status.idle")})` : "");
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

// Las preferencias de la ventana (ocultar desconectados, grupos plegados,
// emojis recientes) se guardan en la configuración para que se conserven al
// reiniciar LanChat: localStorage no sirve porque el puerto cambia en cada inicio.
let prefQueue = Promise.resolve();

// savePref cambia la preferencia en el acto y la guarda. Las peticiones van
// en fila para que, con varios clics seguidos, el último valor sea el guardado.
function savePref(field, value, save) {
  state.self = { ...state.self, [field]: value };
  prefQueue = prefQueue
    .then(() => save(value))
    .catch((e) => showBanner(t("prefs.saveFailed", { err: e.message }), 5000));
}

function hideOffline() {
  return state.self?.hideOffline ?? false;
}

function setHideOffline(hide) {
  savePref("hideOffline", hide, api.hideOffline);
  renderContacts();
}

// visibleContact: al ocultar desconectados se siguen viendo los que tienen
// mensajes sin leer y la conversación abierta; al buscar, todos.
function visibleContact(c, filter) {
  return c.online || filter || !hideOffline() || c.unread > 0 || c.id === state.current;
}
// Claves de los grupos fijos en collapsedGroups (guardadas en la
// configuración: no cambian con el idioma); groupLabel da su nombre.
const NO_GROUP = "Sin grupo";
const ROOMS_GROUP = "Salas";
const CONTACTS_GROUP = "Contactos";

function groupLabel(name) {
  return { [NO_GROUP]: t("group.none"), [ROOMS_GROUP]: t("group.rooms"), [CONTACTS_GROUP]: t("group.contacts") }[name] ?? name;
}

function collapsedGroups() {
  return new Set(state.self?.collapsedGroups ?? []);
}

// toggleGroup pliega o despliega un grupo. De paso olvida los grupos que ya
// no existen, para que la lista guardada no crezca sin fin.
function toggleGroup(name) {
  const set = collapsedGroups();
  if (!set.delete(name)) set.add(name);
  const existing = new Set([...groupNames(), NO_GROUP, ROOMS_GROUP, CONTACTS_GROUP]);
  savePref("collapsedGroups", [...set].filter((g) => existing.has(g)), api.collapsedGroups);
  renderContacts();
}

function groupNames() {
  const names = new Set(sortedContacts().map((c) => c.group).filter(Boolean));
  return [...names].sort(compareNames);
}

// groupHeader muestra "▾ Nombre (en línea/total)"; las salas, solo el total.
function groupHeader(name, members, collapsed, foldable = true) {
  const li = document.createElement("li");
  li.className = "group-header";
  const count = name === ROOMS_GROUP
    ? members.length
    : `${members.filter((c) => c.online).length}/${members.length}`;
  li.textContent = `${foldable ? (collapsed ? "▸ " : "▾ ") : ""}${groupLabel(name)} (${count})`;
  if (foldable) {
    li.title = t(collapsed ? "group.show" : "group.hide");
    li.addEventListener("click", () => toggleGroup(name));
  }
  return li;
}

function renderContacts() {
  const filter = $("search").value.trim().toLocaleLowerCase(i18n.lang);
  const ul = $("contacts");
  ul.replaceChildren();
  const matching = sortedContacts().filter(
    (c) => !filter || `${c.displayName} ${c.detail} ${c.group}`.toLocaleLowerCase(i18n.lang).includes(filter),
  );
  const list = matching.filter((c) => visibleContact(c, filter));
  const hidden = matching.length - list.length;
  const rooms = sortedRooms().filter((r) => !filter || r.name.toLocaleLowerCase(i18n.lang).includes(filter));
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
  btn.title = t(hideOffline() ? "list.showOffline" : "list.hideOffline");
  btn.setAttribute("aria-label", btn.title);

  const total = totalUnread();
  document.title = total > 0 ? `(${total}) LanChat` : "LanChat";
}

// hiddenHint avisa al final de la lista cuántos desconectados no se ven.
function hiddenHint(n) {
  const li = document.createElement("li");
  li.className = "hidden-hint";
  li.append(`${t("list.hiddenOffline", { n })} · `,
    Object.assign(document.createElement("button"), {
      type: "button",
      className: "link-btn",
      textContent: t("list.show"),
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
    return t("contact.incompatibleOlder", { name: c.displayName, theirs, url });
  }
  if (c.incompatible === "newer") {
    return t("contact.incompatibleNewer", { name: c.displayName, theirs, url });
  }
  return "";
}

// incompatibleBadge es la marca roja "incompatible" de la lista.
function incompatibleBadge() {
  const span = document.createElement("span");
  span.className = "incompatible";
  span.textContent = t("contact.incompatible");
  return span;
}

function contactItem(c) {
  const li = document.createElement("li");
  li.className = "contact" + (c.online ? "" : " offline") + (c.id === state.current ? " selected" : "");
  // El equipo, la IP y la versión van en el tooltip (y en el encabezado del
  // chat): la segunda línea es para el último mensaje, como en WhatsApp.
  li.title = [
    c.detail,
    c.online ? statusLabel(c.status, c.statusText) : t("status.offline"),
    c.appVersion && `LanChat ${c.appVersion}`,
  ].filter(Boolean).join("\n");

  const text = document.createElement("div");
  text.className = "text";
  const name = document.createElement("span");
  name.className = "name";
  name.textContent = c.displayName;
  if (c.identityChanged) {
    name.textContent = `⚠ ${c.displayName}`;
    li.title = t("contact.identityChanged");
  }
  text.append(nameRow(name, c), secondRow(contactSecondLine(c), c.unread));
  if (c.incompatible) {
    // En su propia línea para que no la corte el ancho de la lista.
    const line = document.createElement("span");
    line.className = "status-line";
    line.append(incompatibleBadge(), " · " + t(c.incompatible === "older" ? "contact.mustUpdate" : "contact.updateYours"));
    text.append(line);
    li.title = incompatibleText(c);
  }

  const avatar = contactAvatar(c);
  avatar.title = t("profile.open");
  avatar.addEventListener("click", (e) => {
    e.stopPropagation(); // la ficha, no la conversación
    openProfile(c.id);
  });
  li.append(avatar, text);
  li.addEventListener("click", () => openChat(c.id));
  dropOnContact(li, c.id);
  return li;
}

// contactSecondLine: el último mensaje; sin mensajes, su texto de estado si
// está en línea y tiene uno, o su equipo e IP.
function contactSecondLine(c) {
  if (c.last) return previewElement(c.last);
  const span = document.createElement("span");
  span.className = "detail";
  if (c.online && c.statusText) {
    span.classList.add("status-text");
    span.textContent = c.statusText;
  } else {
    span.textContent = c.detail + (c.appVersion ? ` · ${versionLabel(c.appVersion)}` : "");
  }
  return span;
}

// dropOnContact deja soltar archivos y carpetas sobre un contacto de la lista:
// se abre su conversación y se envían como al soltarlos dentro de ella.
function dropOnContact(li, id) {
  li.dataset.peer = id;
  // La lista se redibuja seguido (presencia, mensajes); el resaltado sobrevive.
  if (state.dropTarget === id) li.classList.add("drop-target");
  li.addEventListener("dragover", (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "copy";
    setDropTarget(id);
  });
  li.addEventListener("dragleave", (e) => {
    if (!li.contains(e.relatedTarget) && state.dropTarget === id) setDropTarget(null);
  });
  li.addEventListener("drop", (e) => {
    setDropTarget(null);
    if (!hasFiles(e)) return;
    e.preventDefault();
    uploadDropped(e.dataTransfer, id);
  });
}

function setDropTarget(id) {
  if (state.dropTarget === id) return;
  state.dropTarget = id;
  for (const el of $("contacts").querySelectorAll(".contact[data-peer]")) {
    el.classList.toggle("drop-target", el.dataset.peer === id);
  }
}

const hasFiles = (e) => e.dataTransfer?.types.includes("Files");

// sortedRooms: al final aquellas de las que se salió; si no, la de actividad
// más reciente primero.
function sortedRooms() {
  return [...state.rooms.values()].sort(
    (a, b) => a.left - b.left || lastAt(b) - lastAt(a) ||
      compareNames(a.name, b.name),
  );
}

// memberName es el nombre de un miembro de sala tal como lo ve este usuario.
function memberName(id) {
  if (id === state.self.id) return t("msg.you");
  return state.contacts.get(id)?.displayName ?? t("room.unknownMember");
}

function roomDetail(r) {
  if (r.left) return t("room.left");
  return t("room.members", { n: r.members.length });
}

// NOTICE_KEYS es el texto de cada tipo de aviso de sala (protocol.RoomNotice).
const NOTICE_KEYS = {
  created: "notice.created", added: "notice.added", renamed: "notice.renamed", removed: "notice.removed",
  admin: "notice.admin", unadmin: "notice.unadmin", left: "notice.left",
};

// noticeText es el aviso de sala m en el idioma de la página, con los nombres
// como los ve este usuario. Sin datos (de una versión anterior) o de un tipo
// desconocido, el texto con que llegó.
function noticeText(m) {
  const n = m.notice;
  const key = n && NOTICE_KEYS[n.type];
  if (!key) return m.body;
  const selfName = state.self.name || state.self.hostname;
  const nameOf = (x) => (x.id === state.self.id ? selfName : state.contacts.get(x.id)?.displayName ?? x.name);
  return t(key, {
    who: m.outgoing ? selfName : memberName(m.peerId),
    whom: joinNames((n.members ?? []).map(nameOf)),
    name: n.name ?? "",
  });
}

function roomItem(r) {
  const key = ROOM + r.id;
  const li = document.createElement("li");
  li.className = "contact room" + (r.left ? " offline" : "") + (key === state.current ? " selected" : "");
  li.title = r.members.map(memberName).join(", ");

  const text = document.createElement("div");
  text.className = "text";
  const name = document.createElement("span");
  name.className = "name";
  name.textContent = r.name;
  let second;
  if (r.last && !r.left) {
    second = previewElement(r.last);
  } else {
    second = document.createElement("span");
    second.className = "detail";
    second.textContent = roomDetail(r);
  }
  text.append(nameRow(name, r), secondRow(second, r.unread));
  li.append(roomAvatar(r), text);
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
  $("peer-status").textContent = r.members.map(memberName).join(", ");
  $("peer-detail").hidden = true;
  $("peer-who").title = $("peer-status").textContent;
  $("peer-avatar").replaceChildren(roomAvatar(r));
  $("identity-note").hidden = true;
  $("incompatible-note").hidden = true;
  for (const id of ["room-rename-btn", "room-leave-btn"]) $(id).disabled = r.left;
  syncComposer();
  const note = $("offline-note");
  note.hidden = !r.left;
  note.textContent = t("room.leftNote");
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
  $("peer-avatar").replaceChildren(contactAvatar(c));
  syncComposer(); // a un contacto incompatible no se le puede escribir
  $("incompatible-note").hidden = !c.incompatible;
  $("incompatible-note").textContent = incompatibleText(c);
  $("peer-name").textContent = c.displayName;
  // Tres líneas: nombre, estado (y su mensaje) y equipo; en el teléfono la
  // del equipo se oculta (style.css). El título muestra todo si no cabe.
  $("peer-status").replaceChildren(...statusParts(c));
  let detail = c.detail;
  if (c.appVersion && c.appVersion !== state.self.version) {
    detail += ` · ${t("contact.versionDetail", { theirs: c.appVersion, yours: state.self.version })}`;
  }
  $("peer-detail").textContent = detail;
  $("peer-detail").hidden = false;
  $("peer-who").title = [c.displayName, $("peer-status").textContent, detail].join("\n");
  $("remove-contact-btn").hidden = c.online;
  const note = $("offline-note");
  note.hidden = c.online;
  note.textContent = t("contact.offlineNote", { name: c.displayName });
  $("identity-note").hidden = !c.identityChanged;
  $("identity-note-text").textContent = t("contact.identityNote", { name: c.displayName });
}

function dayLabel(ts) {
  const d = new Date(ts);
  const today = new Date();
  const yesterday = new Date();
  yesterday.setDate(today.getDate() - 1);
  if (d.toDateString() === today.toDateString()) return t("time.today");
  if (d.toDateString() === yesterday.toDateString()) return t("time.yesterday");
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
    div.append(document.createTextNode(noticeText(m)), metaElement(m));
    return div;
  }
  if (m.deleted) {
    const text = "🚫 " + t(m.outgoing ? "msg.youDeleted" : "msg.deleted");
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
    tag.textContent = "📢 " + t("msg.broadcast");
    div.append(tag);
  }
  if (m.forwarded) div.append(forwardedTag());
  if (m.reply) div.append(quoteElement(m.reply));
  div.append(document.createTextNode(m.body), metaElement(m));
  return withActions(div, m);
}

// quoteText es lo que se muestra de un mensaje citado: su texto o, de un
// envío de archivos, su resumen ("📎 informe.pdf (2 MB)").
function quoteText(m) {
  return m.deleted ? "🚫 " + t("msg.deletedQuote") : m.kind === "event" ? noticeText(m) : m.body;
}

// quoteElement es la cita dentro de una respuesta. Si el original está
// cargado se muestra tal como está ahora; si no, lo que guardó el servidor.
// Al tocarla se salta al original.
function quoteElement(r) {
  const orig = findMessage(r.id);
  const q = document.createElement("button");
  q.type = "button";
  q.className = "quote";
  q.title = t("msg.goToOriginal");
  const who = document.createElement("strong");
  who.textContent = memberName(r.author);
  const text = document.createElement("span");
  text.textContent = orig ? quoteText(orig) : r.deleted ? "🚫 " + t("msg.deletedQuote") : r.text;
  q.append(who, text);
  q.addEventListener("click", (e) => {
    if (state.selecting) return; // al seleccionar, tocarla marca el mensaje
    e.stopPropagation();
    jumpToMessage(r.id);
  });
  return q;
}

// jumpToMessage lleva la vista al mensaje id y lo resalta un momento. Si es
// antiguo, carga páginas anteriores hasta encontrarlo.
async function jumpToMessage(id) {
  const key = state.current;
  const conv = state.messages.get(key);
  for (let i = 0; conv && !findMessage(id) && !conv.complete && i < 50; i++) {
    await loadHistory(key, conv.list[0]?.id);
    if (state.current !== key) return;
  }
  const el = [...$("messages").querySelectorAll("[data-id]")].find((x) => x.dataset.id === id);
  if (!el) {
    showBanner(t("msg.originalGone"), 4000);
    return;
  }
  el.scrollIntoView({ block: "center", behavior: "smooth" });
  el.classList.remove("flash");
  void el.offsetWidth; // reinicia la animación si ya estaba resaltado
  el.classList.add("flash");
  setTimeout(() => el.classList.remove("flash"), 1600);
}

function forwardedTag() {
  const tag = document.createElement("span");
  tag.className = "broadcast-tag forwarded-tag";
  tag.textContent = "↪ " + t("msg.forwarded");
  return tag;
}

// ---------- Selector de emojis ----------

// EMOJI_GROUPS (emoji.js) trae todos los emojis de Unicode por grupo. Cada
// sistema sabe dibujar solo una parte (Windows 10 no tiene los más nuevos ni
// las banderas de países): el selector muestra los que aquí sí se ven.

// MAX_RECENT: cuántos emojis recuerda la pestaña Recientes (cuatro renglones).
const MAX_RECENT = 32;
const RECENT_TAB = { key: "emoji.recent", icon: "🕒" };
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
  return state.self?.recentEmoji ?? [];
}

function rememberEmoji(emoji) {
  const list = [emoji, ...recentEmoji().filter((e) => e !== emoji)].slice(0, MAX_RECENT);
  savePref("recentEmoji", list, api.recentEmoji);
}

function renderEmojiPanel() {
  const tabs = [RECENT_TAB, ...EMOJI_GROUPS];
  $("emoji-tabs").replaceChildren(...tabs.map((g) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "emoji-tab" + (g === emojiTab ? " selected" : "");
    b.textContent = g.icon;
    b.title = t(g.key);
    b.setAttribute("role", "tab");
    b.setAttribute("aria-selected", g === emojiTab);
    b.addEventListener("click", () => {
      emojiTab = g;
      renderEmojiPanel();
    });
    return b;
  }));
  $("emoji-title").textContent = t(emojiTab.key);
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
    empty.textContent = t(emojiTab === RECENT_TAB ? "emoji.recentEmpty" : "emoji.groupEmpty");
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

// canReply: se responde a textos y envíos de archivos en una conversación
// donde se puede escribir.
const canReply = (m) => !m.deleted && m.kind !== "event" && canWrite();

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
  btn.title = t("menu.react");
  btn.setAttribute("aria-label", t("menu.reactOrMore"));
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

// ICONS son los trazos (viewBox 24×24) de los íconos de las acciones sobre
// mensajes; se dibujan con el color del texto, así siguen al tema.
const ICONS = {
  reply: "M9 14 4 9l5-5M4 9h10a6 6 0 0 1 6 6v4",
  forward: "m15 14 5-5-5-5M20 9H10a6 6 0 0 0-6 6v4",
  edit: "M4 20h4L19 9l-4-4L4 16v4ZM13.5 6.5l4 4",
  copy: "M10 9h9a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2h-9a2 2 0 0 1-2-2v-8a2 2 0 0 1 2-2ZM5 15H4.5A1.5 1.5 0 0 1 3 13.5v-9A1.5 1.5 0 0 1 4.5 3h9A1.5 1.5 0 0 1 15 4.5V5",
  select: "M5 3h14a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2ZM8 12l3 3 5-6",
  trash: "M4 7h16M9 7V4h6v3M6 7l1 13h10l1-13M10 11v6M14 11v6",
  // Un monitor con una X: borrar solo en este equipo.
  erase: "M5 4h14a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2ZM8 20h8M12 16v4M10 8l4 4M14 8l-4 4",
};

function iconElement(name) {
  const NS = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(NS, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("class", "icon");
  svg.setAttribute("aria-hidden", "true");
  const path = document.createElementNS(NS, "path");
  path.setAttribute("d", ICONS[name]);
  svg.append(path);
  return svg;
}

function closeMsgMenu() {
  $("msg-menu").hidden = true;
  document.querySelector(".msg-act.open")?.classList.remove("open");
}

// openMsgMenu abre el menú del mensaje junto a su botón ☺ (anchor) o, con
// clic derecho, donde está el puntero (at: {x, y}).
function openMsgMenu(id, anchor, showMore = false, at = null) {
  const m = findMessage(id);
  const menu = $("msg-menu");
  if (!m || m.deleted || (!menu.hidden && menu.dataset.id === id && !showMore && !at)) return closeMsgMenu();
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
    b.title = t(emoji === mine ? "menu.removeReaction" : "menu.react");
    b.addEventListener("click", () => react(m, emoji));
    row.append(b);
  }
  if (!showMore) {
    const more = document.createElement("button");
    more.type = "button";
    more.className = "emoji more";
    more.textContent = "＋";
    more.title = t("menu.moreReactions");
    more.addEventListener("click", (e) => {
      e.stopPropagation();
      openMsgMenu(id, anchor, true, at);
    });
    row.append(more);
  }
  menu.replaceChildren(row);
  const item = (icon, label, onClick, danger = false) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "menu-item" + (danger ? " danger" : "");
    b.append(iconElement(icon), label);
    b.addEventListener("click", onClick);
    menu.append(b);
  };
  if (canReply(m)) item("reply", t("menu.reply"), () => startReply(m));
  if (m.kind === "text" || m.transfer) {
    item("copy", t("menu.copy"), () => {
      closeMsgMenu();
      copyText(copyBody(m), t("copy.done", { n: 1 }));
    });
  }
  if (canEdit(m)) item("edit", t("menu.edit"), () => startEdit(m));
  if (canForward(m)) {
    item("forward", t("menu.forward"), () => {
      closeMsgMenu();
      openForward([m.id]);
    });
  }
  item("select", t("menu.select"), () => startSelect(m));
  if (canDelete(m)) item("trash", t("menu.deleteForEveryone"), () => deleteForEveryone(m), true);
  if (canDeleteHere(m)) {
    item("erase", t("menu.deleteHere"), () => {
      closeMsgMenu();
      transferAction("delete", m.id, m.outgoing
        ? t("transfer.deleteHereOut", { name: memberName(m.peerId) })
        : t("transfer.deleteHereIn"));
    }, true);
  }
  menu.hidden = false;
  anchor.classList.add("open");
  const w = menu.offsetWidth;
  const h = menu.offsetHeight;
  if (at) {
    // En el puntero, como el menú contextual, sin salirse de la ventana.
    menu.style.left = `${Math.max(8, Math.min(at.x, innerWidth - w - 8))}px`;
    menu.style.top = `${at.y + h + 8 > innerHeight ? Math.max(8, at.y - h) : at.y}px`;
    return;
  }
  // Junto al botón, sin salirse de la ventana.
  const a = anchor.getBoundingClientRect();
  menu.style.left = `${Math.max(8, Math.min(a.left + a.width / 2 - w / 2, innerWidth - w - 8))}px`;
  menu.style.top = `${a.bottom + h + 8 > innerHeight ? Math.max(8, a.top - h - 6) : a.bottom + 6}px`;
}

// msgContextMenu abre con clic derecho el menú del mensaje (el del botón ☺),
// como en WhatsApp Web. Queda el del navegador si hay texto seleccionado
// (para copiar solo una parte), al seleccionar mensajes y en el teléfono.
function msgContextMenu(e) {
  if (state.mobile || state.selecting || !document.getSelection().isCollapsed) return;
  const el = e.target.closest(".msg[data-id]");
  const btn = el?.querySelector(":scope > .msg-act");
  if (!btn) return; // avisos y eliminados no tienen menú
  e.preventDefault();
  closeMsgMenu();
  openMsgMenu(el.dataset.id, btn, false, { x: e.clientX, y: e.clientY });
}

// copyText copia al portapapeles y lo confirma con un aviso breve.
// Primero con un textarea temporal: es síncrono, así cuenta como parte del
// clic aunque la API del portapapeles no exista o no tenga permiso (p. ej. en
// el WebView de Android).
async function copyText(text, done) {
  const focused = document.activeElement;
  const ta = Object.assign(document.createElement("textarea"), { value: text, readOnly: true });
  ta.style.cssText = "position:fixed;top:0;left:0;opacity:0";
  document.body.append(ta);
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand("copy");
  } catch { /* se intenta con la API */ }
  ta.remove();
  focused?.focus?.({ preventScroll: true });
  if (!ok) {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      return showBanner(t("copy.failed"), 4000);
    }
  }
  showBanner(done, 2000);
}

// copyBody es lo que se copia de un mensaje: su texto o, de un envío de
// archivos, qué se envió ("📎 Envió 3 archivos").
const copyBody = (m) => (m.transfer ? transferText(m.transfer) : m.body);

// IMAGE_EXT: lo que se describe como imagen al copiar un envío.
const IMAGE_EXT = /\.(jpe?g|png|gif|webp|bmp|heic)$/i;

// transferText describe un envío de archivos contando imágenes, archivos y
// carpetas: "📷 Envió una imagen: foto.jpg", "📎 Envió 3 archivos",
// "📁 Envió una carpeta: Facturas (8 archivos)" o, si hay de varios tipos,
// "📎 Envió 2 imágenes, 3 archivos y una carpeta".
function transferText(tr) {
  const images = [];
  const files = [];
  const folders = new Map(); // carpeta raíz -> archivos dentro
  for (const f of tr.files) {
    if (f.dir) {
      const root = f.dir.split("/")[0];
      folders.set(root, (folders.get(root) ?? 0) + 1);
    } else {
      (IMAGE_EXT.test(f.name) ? images : files).push(f.name);
    }
  }
  const kinds = [
    { n: images.length, key: "count.images", icon: "📷" },
    { n: files.length, key: "count.files", icon: "📎" },
    { n: folders.size, key: "count.folders", icon: "📁" },
  ].filter((k) => k.n > 0).map((k) => ({ ...k, text: t(k.key, { n: k.n }) }));
  if (kinds.length === 0) return "📎 " + t("copy.sent", { what: t("count.files", { n: tr.files.length }) });
  if (kinds.length === 1) {
    const k = kinds[0];
    if (k.n > 1) return `${k.icon} ${t("copy.sent", { what: k.text })}`;
    if (folders.size === 1) {
      const [[name, count]] = folders;
      return "📁 " + t("copy.sentFolder", { name, files: t("count.filesNum", { n: count }) });
    }
    return `${k.icon} ${t("copy.sentNamed", { what: k.text, name: images[0] ?? files[0] })}`;
  }
  return "📎 " + t("copy.sent", { what: joinNames(kinds.map((k) => k.text)) });
}

// copySelected copia los mensajes seleccionados como WhatsApp Web: un
// renglón por mensaje, "[hora, fecha] Nombre: texto".
function copySelected() {
  const msgs = selectedMessages();
  if (msgs.length === 0) return;
  const me = state.self.name || state.self.hostname;
  const text = msgs.map((m) => {
    const d = new Date(m.at);
    const who = m.outgoing ? me : memberName(m.peerId);
    return `[${timeFmt.format(d)}, ${copyDateFmt.format(d)}] ${who}: ${copyBody(m)}`;
  }).join("\n");
  endSelect();
  copyText(text, t("copy.done", { n: msgs.length }));
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
  const what = t(m.kind === "files" ? "del.thisTransfer" : "del.thisMessage");
  let question = m.roomId ? t("del.confirmRoom", { what }) : t("del.confirmChat", { what, name: memberName(m.peerId) });
  if (m.transfer?.state === "completed") question += " " + t("del.filesKept");
  if (!confirm(question)) return;
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
    b.title = peers.map((p) => (p === "" ? t("msg.you") : memberName(p))).join(", ");
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
  if (m.edited && !m.deleted) meta.prepend(`${t("msg.edited")} · `);
  if (m.mergedFrom) meta.prepend(mergedElement(m));
  if (m.status === "pending" && m.roomId) meta.title = t("meta.pendingRoom");
  else if (m.status === "pending") meta.title = t("meta.pending");
  else if (m.outgoing && m.readAt) meta.title = t("meta.read", { when: `${dayLabel(m.readAt)} ${timeFmt.format(new Date(m.readAt))}` });
  else if (m.outgoing) meta.title = t("ticks.delivered");
  return meta;
}

// mergedElement: el mensaje era de otro contacto (una identidad vieja que se
// quitó) y se combinó aquí; se dice de quién, para que no pase por escrito
// por este.
function mergedElement(m) {
  const span = document.createElement("span");
  span.className = "merged";
  span.textContent = `⤷ ${t("merged.label", { name: m.mergedFrom })} · `;
  span.title = t(m.outgoing ? "merged.sentTitle" : "merged.receivedTitle", { name: m.mergedFrom });
  return span;
}

// ---------- Render: archivos ----------

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

function transferStatus(m, tr) {
  // Uno combinado fue de otro contacto: se nombra a ese.
  const who = m.mergedFrom?.split(" · ")[0] || (state.contacts.get(m.peerId)?.displayName ?? t("transfer.theContact"));
  const until = t("transfer.until", { when: expiryFmt.format(new Date(tr.expiresAt)) });
  const n = tr.files.length; // elige la forma: "el archivo" o "los archivos"
  if (tr.outgoing) {
    switch (tr.state) {
      case "offered":
        return m.status === "pending"
          ? `${t("transfer.offerQueued", { name: who })} ${until}`
          : `${t("transfer.waiting", { name: who, n })} ${until}`;
      case "downloading": return t("transfer.theyDownload", { name: who });
      case "completed": return t("transfer.theyReceived", { name: who, n }) + " ✓";
      case "rejected": return t("transfer.theyRejected", { name: who, n });
      case "canceled": return t("transfer.canceled");
      case "expired": return t("transfer.expiredUnused");
    }
  } else {
    switch (tr.state) {
      case "offered": return `${t("transfer.wantsToSend", { name: who, n })} ${until}`;
      case "downloading": return t("transfer.downloading");
      case "completed": return t("transfer.savedIn", { dir: tr.dir });
      case "rejected": return t("transfer.youRejected", { n });
      case "canceled": return tr.error || t("transfer.canceled");
      case "expired": return tr.error || t("transfer.expiredAsk");
      case "failed": return t("transfer.failed", { err: tr.error });
    }
  }
  return tr.state;
}

// DELETABLE_TRANSFERS: estados en que una oferta no llegó a nada; se borra
// desde el menú del mensaje (app.DeleteTransfer y app.DeleteForEveryone).
const DELETABLE_TRANSFERS = new Set(["canceled", "rejected", "expired"]);

function transferCard(m) {
  const tr = m.transfer;
  const div = document.createElement("div");
  div.className = `${msgClass(m)} files ${tr.state}`;
  div.dataset.id = m.id;
  if (m.forwarded) div.append(forwardedTag());

  const title = document.createElement("div");
  title.className = "files-title";
  title.textContent = `📎 ${t("count.filesNum", { n: tr.files.length })} · ${fmtSize(tr.total)}`;
  div.append(title);

  const shown = previews(tr);
  if (shown) div.append(shown);
  div.append(fileList(tr));

  if (tr.state === "downloading") {
    const bar = document.createElement("div");
    bar.className = "progress";
    bar.append(document.createElement("div"));
    const info = document.createElement("div");
    info.className = "progress-info";
    div.append(bar, info);
    const done = tr.files.filter((f) => f.done).reduce((n, f) => n + f.size, 0);
    paintProgress(div, state.progress.get(tr.id) ?? { done, total: tr.total, rate: 0 });
  }

  const status = document.createElement("div");
  status.className = "status";
  status.textContent = transferStatus(m, tr);
  div.append(status);

  const actions = document.createElement("div");
  actions.className = "actions-row";
  const act = (label, action, primary, confirmText) =>
    textButton(label, () => transferAction(action, tr.id, confirmText), primary);
  if (!tr.outgoing && tr.state === "offered") {
    const accept = act(t("transfer.accept"), "accept", true);
    accept.title = t("transfer.saveIn", { dir: state.downloadDir || t("transfer.defaultDir") });
    actions.append(accept);
    // En el teléfono no hay selector de carpetas: todo va a Descargas.
    if (!state.mobile) {
      const saveAt = textButton(t("transfer.saveAs"), () => saveAs(tr.id));
      saveAt.title = t("transfer.saveAsTitle");
      actions.append(saveAt);
    }
    actions.append(act(t("transfer.reject"), "reject"));
  } else if (!tr.outgoing && tr.state === "failed") {
    actions.append(act(t("transfer.retry"), "accept", true),
      act(t("transfer.cancel"), "cancel", false, t("transfer.cancelDownloadAsk")));
  } else if (tr.state === "downloading" || (tr.outgoing && tr.state === "offered")) {
    actions.append(act(t(tr.outgoing ? "transfer.cancelSend" : "transfer.cancel"), "cancel", false,
      t(tr.outgoing ? "transfer.cancelSendAsk" : "transfer.cancelDownloadAsk")));
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
function previews(tr) {
  if (!state.self.previews) return null;
  const withThumb = tr.files.filter((f) => f.thumb);
  if (withThumb.length === 0) return null;
  const shown = withThumb.slice(0, MAX_PREVIEWS);
  const box = document.createElement("div");
  box.className = `previews n${shown.length}`;
  shown.forEach((f, i) => {
    const cell = document.createElement("button");
    cell.type = "button";
    cell.className = "preview";
    const img = document.createElement("img");
    img.src = `/api/files/thumb?${new URLSearchParams({ id: tr.id, index: f.index })}`;
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
      cell.title = t("preview.view", { name: f.name });
      cell.addEventListener("click", () => openViewer(tr, f));
    } else {
      cell.title = tr.outgoing ? f.name : t("preview.acceptToView");
      cell.disabled = true;
    }
    box.append(cell);
  });
  return box;
}

// openViewer muestra la imagen completa, leída directo del archivo.
function openViewer(tr, f) {
  const img = $("viewer-img");
  img.onerror = () => {
    $("viewer").close();
    showBanner(t("preview.openFailed"), 5000);
  };
  img.src = `/api/files/view?${new URLSearchParams({ id: tr.id, index: f.index })}`;
  img.alt = f.name;
  $("viewer-name").textContent = f.savedName || f.name;
  $("viewer").showModal();
}

// MAX_FILE_ROWS: filas que muestra una tarjeta antes de resumir "y N más".
const MAX_FILE_ROWS = 10;

// fileList muestra los archivos sueltos y, de cada carpeta, una sola fila.
// Sin escritorio no se pueden abrir carpetas: se lista cada archivo.
function fileList(tr) {
  const ul = document.createElement("ul");
  ul.className = "file-list";
  const rows = [];
  const folders = new Map(); // carpeta raíz -> { count, size, done }
  for (const f of tr.files) {
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
      size.textContent = `${t("count.filesNum", { n: info.count })} · ${fmtSize(info.size)}`;
      li.append(name, size);
      if (!tr.outgoing && info.done > 0 && !state.mobile) {
        li.append(textButton(t("files.openFolder"), () =>
          api.openDir(tr.id, row.folder).catch((e) => showBanner(e.message, 5000))));
      }
    } else {
      const f = row.file;
      name.textContent = (f.dir ? `${f.dir}/` : "") + (f.savedName || f.name);
      name.title = f.savedName && f.savedName !== f.name ? t("files.original", { name: f.name }) : f.name;
      size.textContent = fmtSize(f.size) + (f.done && tr.state !== "completed" ? " ✓" : "");
      li.append(name, size);
      if (!tr.outgoing && f.done) {
        li.append(textButton(t("files.open"), () => openReceived(tr, f, false)));
        if (!state.mobile) li.append(textButton(t("files.showInFolder"), () => openReceived(tr, f, true)));
      }
    }
    ul.append(li);
  }
  if (rows.length > MAX_FILE_ROWS) {
    const li = document.createElement("li");
    li.className = "file-size";
    li.textContent = t("files.andMore", { n: rows.length - MAX_FILE_ROWS });
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
  info.textContent = `${pct.toFixed(0)}% · ${t("transfer.progress", { done: fmtSize(p.done), total: fmtSize(p.total) })}` +
    (p.rate > 0 ? ` · ${fmtSize(Math.round(p.rate))}/s` : "");
}

function cardElement(id) {
  return $("messages").querySelector(`.msg[data-id="${CSS.escape(id)}"]`);
}

// saveAs ("Guardar en…") pregunta la carpeta y acepta la oferta ahí. Si el
// usuario cancela el selector, la oferta queda como estaba.
async function saveAs(id) {
  try {
    await api.saveAs(id);
  } catch (e) {
    showBanner(e.message, 5000);
  }
}

async function transferAction(action, id, confirmText) {
  if (confirmText && !confirm(confirmText)) return;
  try {
    await api.transfer(action, id);
  } catch (e) {
    showBanner(e.message, 5000);
  }
}

async function openReceived(tr, f, reveal) {
  const name = f.savedName || f.name;
  if (!reveal && RISKY_EXT.test(name)) {
    const who = state.contacts.get(tr.peerId)?.displayName;
    const question = who ? t("files.riskyFrom", { file: name, name: who }) : t("files.risky", { file: name });
    if (!confirm(question)) return;
  }
  try {
    await api.openFile(tr.id, f.index, reveal);
  } catch (e) {
    showBanner(e.message, 5000);
  }
}

// Actualiza la oferta guardada en la conversación y su tarjeta en pantalla.
function updateTransfer(tr) {
  if (tr.state !== "downloading") state.progress.delete(tr.id);
  const conv = state.messages.get(tr.peerId);
  const m = conv?.list.find((x) => x.id === tr.id);
  if (!m) return;
  m.transfer = tr;
  const old = tr.peerId === state.current ? cardElement(tr.id) : null;
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

// uploadDropped envía lo soltado a la conversación del contacto to (por
// defecto, la abierta). Si se soltó sobre un contacto de la lista, antes abre
// su conversación.
async function uploadDropped(dt, to = state.current) {
  if (!to) return showBanner(t("upload.pickFirst"), 4000);
  // Las entradas se toman ya, antes de cualquier await: el DataTransfer deja
  // de servir tras el evento.
  const entries = [...dt.items].map((i) => i.webkitGetAsEntry?.()).filter(Boolean);
  const loose = [...dt.files];
  if (to !== state.current) await openChat(to);
  showBanner(t("upload.preparing"));
  let items;
  try {
    items = entries.length ? await droppedFiles(entries) : loose.map((file) => ({ file, dir: "" }));
  } catch (e) {
    return showBanner(t("upload.readFailed", { err: e.message }), 5000);
  }
  await uploadItems(items, to);
}

// uploadPicked envía lo elegido con el selector del sistema (sin escritorio).
function uploadPicked(input) {
  const items = [...input.files].map((file) => ({ file, dir: "" }));
  input.value = ""; // para poder elegir otra vez el mismo archivo
  if (state.current && items.length > 0) uploadItems(items);
}

// uploadItems copia los archivos ([{file, dir}]) a este equipo y los ofrece
// en la conversación de to (por defecto, la abierta).
async function uploadItems(items, to = state.current) {
  if (items.length === 0) return showBanner(t("transfer.emptyFolders"), 5000);
  if (items.length > MAX_OFFER_FILES) {
    return showBanner(t("transfer.tooManyFiles", { max: MAX_OFFER_FILES }), 6000);
  }
  showBanner(t("upload.preparingWhat", { what: items.length === 1 ? items[0].file.name : t("count.filesNum", { n: items.length }) }));
  try {
    addMessage(await api.upload(to, uploadForm(items)));
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
    showBanner(t("share.tooMany", { max: MAX_OFFER_FILES }), 6000);
    return shareFinished();
  }
  currentShare = share;
  // Otro diálogo abierto se cierra; el de compartir se reutiliza (cerrarlo
  // avisaría a la app que este envío terminó).
  const open = document.querySelector("dialog[open]");
  if (open && open.id !== "share-dialog") open.close();
  const total = files.reduce((n, f) => n + Math.max(f.size, 0), 0);
  $("share-title").textContent = files.length === 0 ? t("share.titleText") : t("share.titleFiles", { n: files.length });
  const names = files.slice(0, 3).map((f) => f.name).join(", ");
  const more = files.length > 3 ? " " + t("files.andMore", { n: files.length - 3 }) : "";
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
    err.textContent = t("app.pickContact");
    err.hidden = false;
    return;
  }
  const text = $("share-text-box").hidden ? "" : $("share-text").value.trim();
  $("share-send").disabled = true;
  try {
    let items = [];
    if (share.files?.length) {
      showBanner(t("upload.preparing"));
      items = await Promise.all(share.files.map(async (f) => {
        const resp = await fetch(f.url);
        if (!resp.ok) throw new Error(t("share.readFailed", { name: f.name }));
        return { file: new File([await resp.blob()], f.name), dir: "" };
      }));
    }
    for (const [i, peer] of peers.entries()) {
      if (items.length) {
        showBanner(peers.length === 1 ? t("share.sending") : t("share.sendingTo", { i: i + 1, n: peers.length }));
        addMessage(await api.upload(peer, uploadForm(items)));
      }
      if (text) addMessage(await api.send(peer, text));
    }
    hideBanner();
    $("share-dialog").close();
    if (peers.length === 1) openChat(peers[0]);
    else showBanner(t("share.sent", { n: peers.length }), 4000);
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
  if (state.replyTo) {
    cancelReply();
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
  cancelReply();
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
  cancelReply();
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
  touchConversation(m);
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
    err.textContent = t("protocol.messageTooLong", { max: state.limits.maxMessageBytes / 1024 });
    err.hidden = false;
    return;
  }
  if (state.editing) return saveEdit(body);
  if (!body.trim()) return;
  try {
    const room = roomId(state.current);
    const m = await (state.replyTo ? api.reply(state.replyTo, body)
      : room ? api.roomSend(room, body) : api.send(state.current, body));
    input.value = "";
    autoGrow();
    cancelReply();
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
  const box = $("input");
  box.style.height = "auto";
  box.style.height = `${box.scrollHeight}px`;
}

// ---------- Editar un mensaje ----------

// startEdit pone el texto del mensaje en la caja de escribir; Enter guarda
// y Esc cancela (como en WhatsApp).
function startEdit(m) {
  closeMsgMenu();
  endSelect();
  cancelReply();
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
    err.textContent = t("edit.empty");
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

// ---------- Responder a un mensaje ----------

// startReply pone la cita del mensaje sobre la caja de escribir: lo que se
// envíe a continuación será la respuesta (como en WhatsApp).
function startReply(m) {
  closeMsgMenu();
  endSelect();
  cancelEdit();
  state.replyTo = m.id;
  $("reply-who").textContent = m.outgoing ? t("reply.toYours") : t("reply.to", { name: memberName(m.peerId) });
  $("reply-original").textContent = quoteText(m);
  $("send-error").hidden = true;
  syncComposer();
  $("input").focus();
}

function cancelReply() {
  if (!state.replyTo) return;
  state.replyTo = null;
  syncComposer();
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
  $("reply-bar").hidden = $("composer").hidden || !state.replyTo;
  $("send-btn").textContent = t(state.editing ? "composer.save" : "composer.send");
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
  $("select-n").textContent = n;
  $("select-word").textContent = " " + t("select.selected", { n });
  const forward = n > 0 && msgs.every(canForward);
  setUnavailable($("select-forward"), !forward, t("select.cantForward"));
  const del = n > 0 && msgs.every(canDelete);
  setUnavailable($("select-delete"), !del,
    t("select.cantDelete", { min: Math.round(state.limits.deleteWindowMs / 60000) }));
}

// setUnavailable apaga un botón sin desactivarlo: uno desactivado no responde
// al tocarlo y en el teléfono no hay mouse para ver su title, así que el clic
// muestra la razón (ver unavailableReason).
function setUnavailable(b, off, reason) {
  b.setAttribute("aria-disabled", off ? "true" : "false");
  b.title = off ? reason : "";
}

// unavailableReason, en el clic de un botón apagado con setUnavailable, avisa
// por qué no se puede y devuelve true.
function unavailableReason(b) {
  if (b.getAttribute("aria-disabled") !== "true") return false;
  showBanner(b.title, 5000);
  return true;
}

async function deleteSelected() {
  const msgs = selectedMessages();
  if (msgs.length === 0) return;
  // El plazo pudo vencer mientras estaban seleccionados.
  if (!msgs.every(canDelete)) {
    updateSelectBar();
    return showBanner($("select-delete").title, 5000);
  }
  const what = t("del.theseMessages", { n: msgs.length });
  let question = msgs[0].roomId ? t("del.confirmRoom", { what })
    : t("del.confirmChat", { what, name: memberName(msgs[0].peerId) });
  if (msgs.some((m) => m.transfer?.state === "completed")) question += " " + t("del.filesKept");
  if (!confirm(question)) return;
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
    showBanner(t("del.someFailed", { n: errors.length, err: errors[0] }), 6000);
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
  $("forward-title").textContent = t("forward.title", { n: msgs.length });
  $("forward-what").textContent = forwardSummary(msgs);
  const rooms = sortedRooms().filter((r) => !r.left).map((r) => roomPickItem(r, withFiles));
  $("forward-list").replaceChildren(...rooms, ...sortedContacts().map(pickItem));
  $("forward-help").textContent = t(withFiles ? "forward.helpFiles" : "forward.help");
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
    return `📎 ${cut(files.slice(0, 3).map((f) => f.name).join(", "), 80)}${files.length > 3 ? " " + t("files.andMore", { n: files.length - 3 }) : ""}`;
  });
  if (msgs.length > 3) lines.push("… " + t("files.andMore", { n: msgs.length - 3 }));
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
    detail.append(" · " + t("forward.roomNoFiles"));
    label.title = t("forward.roomNoFilesTitle");
  }
  label.append(box, icon, name, detail);
  li.append(label);
  return li;
}

function filterForward() {
  const q = $("forward-search").value.trim().toLocaleLowerCase(i18n.lang);
  for (const li of $("forward-list").children) {
    li.hidden = q !== "" && !li.textContent.toLocaleLowerCase(i18n.lang).includes(q);
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
    err.textContent = t("forward.pickTarget");
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
      err.textContent = t("forward.partial", { err: res.error });
      err.hidden = false;
      return;
    }
    $("forward-dialog").close();
    const targets = [...peers, ...rooms.map((r) => ROOM + r)];
    if (targets.length === 1) openChat(targets[0]);
    else showBanner(t("forward.done", { n: targets.length }), 3000);
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
  $("name-help").textContent = t("settings.nameHelp", { host: s.hostname });
  $("peers-input").value = (state.manualPeers ?? []).join("\n");
  $("download-input").value = state.downloadDir;
  $("receipts-input").checked = state.self.readReceipts;
  $("previews-input").checked = state.self.previews;
  $("thumbs-usage").textContent = "";
  $("thumbs-clear-btn").disabled = true;
  api.thumbsUsage().then(renderThumbsUsage).catch(() => {});
  $("about").textContent = [`LanChat ${s.version} · ${s.hostname}`, s.copyright, s.license].join("\n");
  $("settings-error").hidden = true;
  renderPhotoSettings();
  renderLanguage();
  renderAndroid();
  renderUpdate();
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

// ---------- Actualizaciones ----------

// renderUpdate muestra en Ajustes la versión, la búsqueda y la descarga.
function renderUpdate() {
  const u = state.update;
  $("update-section").hidden = !u.supported && !u.reason;
  const btn = $("update-btn");
  $("autoupdate-input").checked = u.auto;
  $("autoupdate-input").disabled = !u.supported;
  btn.hidden = !u.supported;
  if (!u.supported) {
    $("update-status").textContent = u.reason;
    return;
  }
  const canInstall = !!u.latest && u.latest !== u.current &&
    (u.phase === "available" || u.phase === "error" || (u.phase === "installing" && state.mobile));
  btn.textContent = canInstall ? t("update.installTo", { version: u.latest }) : t("update.check");
  btn.dataset.action = canInstall ? "install" : "check";
  btn.disabled = u.phase === "checking" || u.phase === "downloading" || (u.phase === "installing" && !state.mobile);
  $("update-status").textContent = updateStatusText(u);
}

function updateStatusText(u) {
  switch (u.phase) {
    case "checking": return t("update.checking");
    case "current": return t("update.current", { version: u.current }) + " ✓";
    case "available": return t("update.availableStatus", { latest: u.latest, current: u.current });
    case "downloading": return t("update.downloading", { version: u.latest, percent: u.percent });
    case "installing": return t(state.mobile ? "update.confirmAndroid" : "update.installing");
    case "error": return u.error;
    default: return t("update.version", { version: u.current });
  }
}

// updateChanged recibe el estado nuevo (evento "update"): actualiza Ajustes,
// muestra el avance de la descarga y pregunta si hay una versión nueva.
function updateChanged(u) {
  const prev = state.update?.phase;
  state.update = u;
  if ($("settings").open) renderUpdate();
  if (u.phase === "downloading" || (u.phase === "installing" && !state.mobile)) {
    showBanner(updateStatusText(u));
  } else if (u.phase === "installing") {
    showBanner(updateStatusText(u), 8000);
  } else if (u.phase === "error" && (prev === "downloading" || prev === "installing")) {
    showBanner(u.error, 10000);
  } else if (prev === "downloading") {
    hideBanner();
  }
  askUpdate();
}

// askUpdate pregunta si se instala la versión nueva. Cerrar con Esc cuenta
// como "Ahora no": no se vuelve a preguntar hasta reabrir LanChat.
function askUpdate() {
  const u = state.update;
  const dialog = $("update-dialog");
  if (!u?.ask || dialog.open) return;
  $("update-text").textContent = t("update.ask", { latest: u.latest, current: u.current }) + " " +
    t(state.mobile ? "update.askAndroid" : "update.askDesktop");
  dialog.returnValue = "";
  dialog.addEventListener("close", () => {
    const action = dialog.returnValue === "install" ? api.updateInstall() : api.updateLater();
    action.catch((e) => showBanner(e.message, 6000));
  }, { once: true });
  dialog.showModal();
}

async function updateButton() {
  try {
    updateChanged(await ($("update-btn").dataset.action === "install" ? api.updateInstall() : api.updateCheck()));
  } catch (e) {
    settingsError(e);
  }
}

async function toggleAutoUpdate() {
  const input = $("autoupdate-input");
  try {
    updateChanged(await api.autoUpdate(input.checked));
  } catch (e) {
    input.checked = state.update.auto;
    settingsError(e);
  }
}

function renderSystem(sys) {
  $("system-section").hidden = !sys.supported;
  $("uninstall-row").hidden = !sys.supported;
  $("autostart-input").checked = sys.autostart;
  $("firewall-status").textContent = sys.firewall ? t("system.firewallAllowed") + " ✓" : t("system.firewallNotSet");
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
  $("battery-status").textContent = restricted ? t("android.batteryRestricted") : t("android.batteryFree") + " ✓";
  $("battery-btn").hidden = !restricted;
}

// ---------- Foto de perfil ----------

function selfAvatar() {
  const s = state.self;
  return avatarElement(s.id, s.name || s.hostname, photoURL(s.id, s.photo));
}

// renderPhotoSettings muestra en Ajustes la foto propia (o las iniciales).
function renderPhotoSettings() {
  const has = Boolean(state.self.photo);
  const big = selfAvatar();
  big.classList.add("big");
  $("settings-avatar").replaceChildren(big);
  $("photo-pick").textContent = t(has ? "photo.change" : "photo.pick");
  $("photo-remove").hidden = !has;
  $("photo-help").textContent = t(has ? "photo.helpSet" : "photo.helpNone");
}

// renderLanguage llena la lista de idiomas de Ajustes: el del sistema y cada
// idioma traducido, con su nombre en ese idioma.
function renderLanguage() {
  const select = $("language-input");
  select.replaceChildren(new Option(t("settings.languageAuto"), ""),
    ...(state.languages ?? []).map((l) => new Option(l.name, l.code)));
  select.value = state.self.language ?? "";
}

// changeLanguage aplica el idioma elegido: la página se vuelve a cargar con
// los textos nuevos.
async function changeLanguage() {
  try {
    await api.language($("language-input").value);
    location.reload();
  } catch (e) {
    settingsError(e);
  }
}

// photoChanged guarda la respuesta del servidor y repinta donde se ve la foto.
function photoChanged(self) {
  state.self = self;
  renderSelf();
}

async function uploadPhoto() {
  const input = $("photo-input");
  const file = input.files[0];
  input.value = ""; // para poder elegir de nuevo la misma imagen
  if (!file) return;
  $("settings-error").hidden = true;
  $("photo-pick").disabled = true;
  try {
    photoChanged(await api.photo(file));
  } catch (e) {
    settingsError(e);
  } finally {
    $("photo-pick").disabled = false;
  }
}

async function removePhoto() {
  $("settings-error").hidden = true;
  try {
    photoChanged(await api.removePhoto());
  } catch (e) {
    settingsError(e);
  }
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
    ? t("thumbs.none")
    : t("thumbs.stored", { n: u.count, size: fmtSize(u.bytes) });
  $("thumbs-clear-btn").disabled = u.count === 0;
}

// clearThumbs borra las miniaturas ya guardadas; las tarjetas cargadas dejan
// de mostrarlas sin esperar a recargar la conversación.
async function clearThumbs() {
  if (!confirm(t("thumbs.clearAsk"))) return;
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
  $("firewall-status").textContent = t("system.waitingAdmin");
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
  $("about-version").textContent = t("update.version", { version: s.version });
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
    err.textContent = t("app.pickContact");
    err.hidden = false;
    return;
  }
  try {
    const res = await api.sendMany(peers, body);
    for (const m of res.messages) addMessage(m);
    if (res.error) {
      err.textContent = t("many.partial", { n: res.messages.length, err: res.error });
      err.hidden = false;
      return;
    }
    $("many-text").value = "";
    $("many-dialog").close();
    showBanner(t("many.sent", { n: res.messages.length }), 3000);
  } catch (e) {
    err.textContent = e.message;
    err.hidden = false;
  }
}

function openIdentity(id = state.current) {
  const c = state.contacts.get(id);
  if (!c) return;
  $("identity-title").textContent = t("identity.title", { name: c.displayName });
  $("identity-theirs").textContent = c.fingerprint || `(${t("identity.unknownYet")})`;
  $("identity-new-box").hidden = !c.identityChanged;
  $("identity-new").textContent = c.newFingerprint;
  $("identity-mine").textContent = state.self.fingerprint;
  $("identity-dialog").showModal();
}

async function trustIdentity() {
  const c = state.contacts.get(state.current);
  if (!c) return;
  if (!confirm(t("identity.trustAsk", { name: c.displayName }))) return;
  try {
    await request("POST", "/api/trust", { peer: c.id });
  } catch (e) {
    showBanner(e.message, 5000);
  }
}

function openGroup() {
  const c = state.contacts.get(state.current);
  if (!c) return;
  $("group-label").textContent = t("group.labelFor", { name: c.displayName });
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

// ---------- Ficha de contacto ----------

// openProfile muestra la ficha de un contacto, como al tocar una foto en
// WhatsApp: su foto en grande, su mensaje de estado y los datos de su equipo.
function openProfile(id) {
  if (!state.contacts.has(id)) return;
  state.profile = id;
  renderProfile();
  $("profile-dialog").showModal();
}

function renderProfile() {
  const c = state.contacts.get(state.profile);
  if (!c) return $("profile-dialog").close();
  const photo = avatarElement(c.id, c.displayName, photoURL(c.id, c.photo));
  $("profile-photo").replaceChildren(photo);
  $("profile-name").textContent = (c.identityChanged ? "⚠ " : "") + c.displayName;

  const dot = document.createElement("span");
  dot.className = dotClass(c.online, c.status);
  let presence = c.online ? statusLabel(c.status) : t("status.offline");
  if (!c.online && c.lastSeen > 0) {
    // Tras los dos puntos va en minúscula ("ayer"), salvo en inglés ("Yesterday").
    let day = dayLabel(c.lastSeen);
    if (!i18n.lang.startsWith("en")) day = day.charAt(0).toLocaleLowerCase(i18n.lang) + day.slice(1);
    presence += " · " + t("profile.lastSeen", { when: `${day}, ${timeFmt.format(c.lastSeen)}` });
  }
  $("profile-presence").replaceChildren(dot, presence);
  $("profile-status").hidden = !(c.online && c.statusText);
  $("profile-status").textContent = c.statusText;

  const fields = [];
  const add = (key, value, opts = {}) => {
    if (!value) return;
    const dt = document.createElement("dt");
    dt.textContent = t(key);
    const box = document.createElement("dd");
    box.textContent = value;
    if (opts.copy) {
      box.classList.add("copyable");
      box.title = t("profile.copy");
      box.addEventListener("click", () => copyText(value, t("profile.copied", { what: value })));
    }
    fields.push(dt, box);
  };
  // El nombre que eligió el otro usuario, si lo renombraste.
  if (c.alias) add("profile.name", c.name);
  add("profile.group", c.group);
  add("profile.host", c.hostname, { copy: true });
  add("profile.ip", c.ip, { copy: true });
  add("profile.version", c.appVersion);
  $("profile-fields").replaceChildren(...fields);
  copyableFingerprint($("profile-fingerprint"), c.fingerprint);
  $("profile-new-box").hidden = !c.identityChanged;
  copyableFingerprint($("profile-new"), c.identityChanged ? c.newFingerprint : "");
  $("profile-chat").hidden = c.id === state.current;
}

// copyableFingerprint muestra una huella que se copia con un clic (o
// "todavía no se conoce" si el contacto aún no la anunció).
function copyableFingerprint(el, value) {
  el.textContent = value || `(${t("identity.unknownYet")})`;
  el.classList.toggle("copyable", Boolean(value));
  el.title = value ? t("profile.copy") : "";
  el.onclick = value ? () => copyText(value, t("profile.copied", { what: t("profile.fingerprint") })) : null;
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
  const titles = { create: t("roomDialog.createTitle"), add: t("roomDialog.addTitle", { name: r?.name }), rename: t("roomDialog.renameTitle") };
  $("room-title").textContent = titles[mode];
  $("room-save").textContent = t({ create: "roomDialog.create", add: "roomDialog.add", rename: "composer.save" }[mode]);
  $("room-name-box").hidden = mode === "add";
  $("room-members-box").hidden = mode === "rename";
  $("room-name").value = mode === "rename" ? r.name : "";
  $("room-name").maxLength = state.limits.maxName;
  const members = new Set(mode === "add" ? r.members : []);
  const candidates = sortedContacts().filter((c) => !members.has(c.id));
  $("room-list").replaceChildren(...candidates.map(pickItem));
  $("room-help").textContent = mode === "create"
    ? t("roomDialog.createHelp", { max: MAX_ROOM_MEMBERS })
    : t("roomDialog.addHelp");
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
  $("members-title").textContent = t("members.title", { name: r.name, n: r.members.length });
  $("members-list").replaceChildren(...r.members.map((id) => {
    const li = document.createElement("li");
    const name = document.createElement("span");
    name.className = "name";
    name.textContent = id === me ? `${state.self.name || state.self.hostname} (${t("members.you")})` : memberName(id);
    if (state.contacts.has(id)) {
      name.classList.add("profile-link");
      name.title = t("profile.open");
      name.addEventListener("click", () => openProfile(id));
    }
    li.append(name);
    const admin = r.admins.includes(id);
    if (admin) {
      const tag = document.createElement("span");
      tag.className = "admin-tag";
      tag.textContent = t("members.admin");
      li.append(tag);
    }
    if (iAdmin && id !== me) {
      li.append(textButton(t(admin ? "members.unadmin" : "members.makeAdmin"), () =>
        memberAction("admin", r.id, { member: id, admin: !admin })));
      const kick = textButton(t("members.remove"), () => {
        if (confirm(t("members.removeAsk", { name: memberName(id), room: r.name }))) {
          memberAction("kick", r.id, { member: id });
        }
      });
      kick.classList.add("danger-btn");
      li.append(kick);
    }
    return li;
  }));
  $("members-help").textContent = t(r.left ? "members.helpLeft" : iAdmin ? "members.helpAdmin" : "members.help");
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
  if (!r || !confirm(t("room.destroyAsk", { name: r.name }))) return;
  $("members-dialog").close();
  try {
    await api.room("destroy", r.id); // el evento "cleared" la quita de la lista
  } catch (e) {
    showBanner(e.message, 5000);
  }
}

async function leaveRoom() {
  const r = currentRoom();
  if (!r || !confirm(t("room.leaveAsk", { name: r.name }))) return;
  try {
    await api.room("leave", r.id);
  } catch (e) {
    showBanner(e.message, 5000);
  }
}

// ---------- Privacidad ----------


// exportData descarga la copia de la base (el navegador la guarda en Descargas).
function exportData() {
  if (state.mobile) return saveExport();
  const a = document.createElement("a");
  a.href = "/api/data/export";
  a.download = "";
  document.body.append(a);
  a.click();
  a.remove();
  showBanner(t("export.downloading"), 4000);
}

// saveExport: sin escritorio (la WebView no descarga), el núcleo guarda la
// copia directamente en la carpeta de archivos recibidos.
async function saveExport() {
  showBanner(t("export.saving"));
  try {
    const { name, dir } = await request("POST", "/api/data/export", {});
    showBanner(t("export.saved", { path: `${friendlyDir(dir)} › ${name}` }), 8000);
  } catch (e) {
    showBanner(e.message, 6000);
  }
}

// friendlyDir muestra la carpeta como el explorador de archivos de Android.
function friendlyDir(dir) {
  return dir.endsWith("/Download/LanChat") ? t("android.downloadsDir") : dir;
}

async function deleteChat() {
  const room = currentRoom();
  const c = state.contacts.get(state.current);
  let question;
  if (room) {
    question = t(room.left ? "clear.roomLeftAsk" : "clear.roomAsk", { name: room.name });
  } else if (c) {
    question = t("clear.chatAsk", { name: c.displayName });
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
  $("remove-title").textContent = t("remove.title", { name: c.displayName });
  const candidates = mergeCandidates(c);
  const select = $("remove-into");
  select.replaceChildren(...candidates.map((o) => Object.assign(document.createElement("option"), {
    value: o.id,
    textContent: `${o.displayName} · ${o.detail}${o.online ? "" : ` (${t("status.offline").toLocaleLowerCase(i18n.lang)})`}`,
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
  const same = (x, y) => Boolean(x) && x.toLocaleLowerCase(i18n.lang) === y.toLocaleLowerCase(i18n.lang);
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
      !confirm(t("remove.deleteAsk", { name: c.displayName }))) return;
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
  const conv = ev.room ? state.rooms.get(ev.room) : state.contacts.get(ev.peer);
  if (conv) conv.last = null; // sin mensajes, vuelve a su lugar por nombre
  if (key !== state.current) {
    state.messages.delete(key);
    renderContacts();
    return;
  }
  if (ev.removed) return showEmpty();
  renderContacts();
  state.messages.set(key, { list: [], complete: true });
  renderMessages();
}

function openWipe() {
  $("settings").close();
  $("wipe-input").value = "";
  $("wipe-label").textContent = t("wipe.typeWord", { word: t("wipe.word") });
  $("wipe-btn").disabled = true;
  $("wipe-downloads").textContent = state.downloadDir || t("transfer.defaultDir");
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
  showBanner(t("wipe.done"), 5000);
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
    showBanner(t("api.reconnecting"));
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
    if (c.id === state.profile && $("profile-dialog").open) renderProfile();
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
    // Se cambió el idioma (en esta u otra ventana): textos nuevos.
    if (state.self.lang && state.self.lang !== i18n.lang) return location.reload();
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
  es.addEventListener("update", (e) => updateChanged(JSON.parse(e.data)));

  // El desinstalador cerró LanChat: la ventana se cierra para soltar su perfil.
  es.addEventListener("quit", () => {
    es.close();
    window.close();
    showBanner(t("api.closed"));
  });
}

// ---------- Inicio ----------

function bind() {
  $("search").addEventListener("input", renderContacts);
  $("offline-btn").addEventListener("click", () => setHideOffline(!hideOffline()));
  $("settings-btn").addEventListener("click", openSettings);
  $("me-avatar").addEventListener("click", openSettings);
  $("photo-pick").addEventListener("click", () => $("photo-input").click());
  $("photo-input").addEventListener("change", uploadPhoto);
  $("photo-remove").addEventListener("click", removePhoto);
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
  // Clic derecho sobre un mensaje: su menú. En otro lado, cierra el abierto.
  $("messages").addEventListener("contextmenu", msgContextMenu);
  document.addEventListener("contextmenu", (e) => {
    if (!e.defaultPrevented && !$("msg-menu").contains(e.target)) closeMsgMenu();
  });
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
    ($("wipe-btn").disabled = $("wipe-input").value.trim() !== t("wipe.word")));
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
  $("identity-btn").addEventListener("click", () => openIdentity());
  // Ficha: el avatar y el nombre del encabezado; en una sala, sus miembros.
  for (const id of ["peer-avatar", "peer-who"]) {
    $(id).addEventListener("click", () => (roomId(state.current) ? openMembers() : openProfile(state.current)));
  }
  $("profile-chat").addEventListener("click", () => {
    $("profile-dialog").close();
    openChat(state.profile);
  });
  $("profile-identity").addEventListener("click", () => openIdentity(state.profile));
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
    } else if (e.key === "Escape" && state.replyTo) {
      cancelReply();
    }
  });
  $("edit-cancel").addEventListener("click", cancelEdit);
  $("reply-cancel").addEventListener("click", () => {
    cancelReply();
    $("input").focus();
  });
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
  $("select-delete").prepend(iconElement("trash"));
  $("select-forward").prepend(iconElement("forward"));
  $("select-copy").prepend(iconElement("copy"));
  $("select-delete").addEventListener("click", deleteSelected);
  $("select-copy").addEventListener("click", copySelected);
  // Ctrl+C al seleccionar copia los mensajes, como en WhatsApp Web (salvo que
  // haya texto seleccionado o se esté escribiendo).
  document.addEventListener("keydown", (e) => {
    if (!state.selecting || !(e.ctrlKey || e.metaKey) || e.key.toLowerCase() !== "c" || e.altKey || e.shiftKey) return;
    if (document.querySelector("dialog[open]") || e.target.closest?.("input, textarea, [contenteditable]")) return;
    if (!document.getSelection().isCollapsed) return;
    e.preventDefault();
    copySelected();
  });
  $("select-forward").addEventListener("click", (e) => {
    if (!unavailableReason(e.currentTarget)) openForward(selectedMessages().map((m) => m.id));
  });
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
  $("update-btn").addEventListener("click", updateButton);
  $("autoupdate-input").addEventListener("change", toggleAutoUpdate);
  $("thumbs-clear-btn").addEventListener("click", clearThumbs);
  $("firewall-btn").addEventListener("click", allowFirewall);
  $("battery-btn").addEventListener("click", () => window.LanChatAndroid?.allowBackground());
  $("app-settings-btn").addEventListener("click", () => window.LanChatAndroid?.openAppSettings());
  $("stop-btn").addEventListener("click", () => window.LanChatAndroid?.stop());
  $("android-autostart").addEventListener("change", (e) =>
    window.LanChatAndroid?.setAutostart(e.currentTarget.checked));
  $("language-input").addEventListener("change", changeLanguage);
  $("open-downloads").addEventListener("click", () =>
    api.openDownloadDir().catch((e) => showBanner(e.message, 5000)));

  // Arrastrar y soltar archivos sobre la conversación.
  const chat = $("chat");
  let dragDepth = 0;
  chat.addEventListener("dragenter", (e) => {
    if (!hasFiles(e) || !state.current || roomId(state.current)) return;
    dragDepth++;
    $("drop-overlay").textContent = t("drop.sendTo", { name: state.contacts.get(state.current)?.displayName ?? "" });
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
loadLocale()
  .catch((e) => console.error("textos:", e)) // sin textos se ven las claves, pero LanChat sirve
  .then(loadState)
  .then(connectEvents)
  .then(askAutostart)
  .then(askUpdate)
  .catch((e) => showBanner(e.message));
