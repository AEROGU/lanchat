// Interfaz de LanChat. Habla con el servidor local:
//   GET  /api/state, /api/messages        estado y conversaciones
//   POST /api/messages, /api/read, ...    acciones
//   /api/rooms...                         salas de chat grupales
//   GET  /api/events (Server-Sent Events)  cambios en vivo
// El texto de los usuarios siempre se inserta con textContent, nunca como HTML.

const $ = (id) => document.getElementById(id);

const state = {
  self: null,
  limits: { maxName: 64, maxMessageBytes: 65536 },
  contacts: new Map(), // id -> contacto
  rooms: new Map(), // id -> sala
  // current es la conversación abierta: el id del contacto o ROOM + id de la sala.
  current: null,
  messages: new Map(), // conversación -> { list: [], complete: bool }
  progress: new Map(), // id de transferencia -> { done, total, rate }
  downloadDir: "",
  expired: false,
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
  readReceipts: (enabled) => request("POST", "/api/read-receipts", { enabled }),
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
  createRoom: (name, members) => request("POST", "/api/rooms", { name, members }),
  roomHistory: (room, before) =>
    request("GET", `/api/rooms/messages?${new URLSearchParams({ room, before: before ?? "" })}`),
  roomSend: (room, body) => request("POST", "/api/rooms/messages", { room, body }),
  // action: members, rename, leave o read.
  room: (action, room, extra) => request("POST", `/api/rooms/${action}`, { room, ...extra }),
  deleteConversation: (peer) => request("POST", "/api/conversation/delete", { peer }),
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

function sortedContacts() {
  return [...state.contacts.values()].sort(
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
  const names = new Set([...state.contacts.values()].map((c) => c.group).filter(Boolean));
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
  const list = sortedContacts().filter(
    (c) => !filter || `${c.displayName} ${c.detail} ${c.group}`.toLocaleLowerCase("es").includes(filter),
  );
  const rooms = sortedRooms().filter((r) => !filter || r.name.toLocaleLowerCase("es").includes(filter));
  const collapsed = filter ? new Set() : collapsedGroups(); // al buscar se muestra todo
  if (rooms.length > 0) {
    ul.append(groupHeader(ROOMS_GROUP, rooms, collapsed.has(ROOMS_GROUP)));
    if (!collapsed.has(ROOMS_GROUP)) for (const r of rooms) ul.append(roomItem(r));
  }
  const names = groupNames();
  if (names.length === 0) {
    if (rooms.length > 0 && list.length > 0) ul.append(groupHeader(CONTACTS_GROUP, list, false, false));
    for (const c of list) ul.append(contactItem(c));
  } else {
    for (const name of [...names, NO_GROUP]) {
      const members = list.filter((c) => (c.group || NO_GROUP) === name);
      if (members.length === 0) continue;
      ul.append(groupHeader(name, members, collapsed.has(name)));
      if (!collapsed.has(name)) for (const c of members) ul.append(contactItem(c));
    }
  }
  $("no-contacts").hidden = state.contacts.size > 0;

  const total = totalUnread();
  document.title = total > 0 ? `(${total}) LanChat` : "LanChat";
}

function contactItem(c) {
  const li = document.createElement("li");
  li.className = "contact" + (c.online ? "" : " offline") + (c.id === state.current ? " selected" : "");
  li.title = c.online ? statusLabel(c.status, c.statusText) : "Desconectado";

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
  text.append(name, detail);
  if (c.online && c.statusText) {
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
  if (r.left) return "Saliste de esta sala";
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
  for (const id of ["room-add-btn", "room-rename-btn", "room-leave-btn"]) $(id).disabled = r.left;
  $("composer").hidden = r.left;
  const note = $("offline-note");
  note.hidden = !r.left;
  note.textContent = "Saliste de esta sala: ya no recibes sus mensajes. Para volver, pide a un miembro que te agregue.";
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
  $("composer").hidden = false;
  $("peer-name").textContent = c.displayName;
  let detail = c.detail;
  if (c.online) detail += ` · ${statusLabel(c.status, c.statusText)}`;
  if (c.appVersion && c.appVersion !== state.self.version) {
    detail += ` · versión ${c.appVersion} (la tuya es ${state.self.version})`;
  }
  $("peer-detail").textContent = detail;
  $("peer-dot").className = dotClass(c.online, c.status);
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
  if (m.kind === "files" && m.transfer) return transferCard(m);
  const div = document.createElement("div");
  div.className = msgClass(m) + (m.kind === "event" ? " event" : "");
  div.dataset.id = m.id;
  if (m.kind === "event") {
    div.append(document.createTextNode(m.body), metaElement(m));
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
  div.append(document.createTextNode(m.body), metaElement(m));
  return div;
}

// msgClass: dirección, estado de entrega y si el destinatario ya lo leyó.
function msgClass(m) {
  return `msg ${m.outgoing ? "out" : "in"} ${m.status}` + (m.readAt ? " read" : "");
}

function metaElement(m) {
  const meta = document.createElement("span");
  meta.className = "meta";
  meta.textContent = timeFmt.format(new Date(m.at));
  if (m.status === "pending" && m.roomId) meta.title = "Pendiente: falta entregarlo a algún miembro desconectado";
  else if (m.status === "pending") meta.title = "Pendiente: se entregará cuando el contacto se conecte";
  else if (m.outgoing && m.readAt) meta.title = `Leído: ${dayLabel(m.readAt)} ${timeFmt.format(new Date(m.readAt))}`;
  else if (m.outgoing) meta.title = "Entregado";
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
  div.className = `${msgClass(m)} files ${t.state}`;
  div.dataset.id = m.id;

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
  const form = new FormData();
  for (const { file, dir } of items) form.append(dir ? `dir:${dir}` : "files", file, file.name);
  showBanner(`Preparando ${items.length === 1 ? items[0].file.name : `${items.length} archivos`}…`);
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
  if (state.current && NARROW.matches) {
    closeChat();
    return true;
  }
  return false;
};

async function openChat(id) {
  setMenu(false);
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
  $("offline-note").hidden = true;
  $("identity-note").hidden = true;
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
    m.transfer ??= old.transfer; // conservar la oferta si el evento no la trae
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
  if (!body.trim() || !state.current) return;
  if (encoder.encode(body).length > state.limits.maxMessageBytes) {
    err.textContent = `El mensaje es demasiado largo (máximo ${state.limits.maxMessageBytes / 1024} KB).`;
    err.hidden = false;
    return;
  }
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
  $("about").textContent = [`LanChat ${s.version} · ${s.hostname}`, s.copyright, s.license].join("\n");
  $("settings-error").hidden = true;
  renderAndroid();
  $("settings").showModal();
  api.system().then(renderSystem).catch(() => {});
}

function renderSystem(sys) {
  $("system-section").hidden = !sys.supported;
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
  label.append(box, dot, name, detail);
  li.append(label);
  return li;
}

function selectMany(which, group) {
  for (const box of $("many-list").querySelectorAll("input")) {
    box.checked = which === "all" ||
      (which === "online" && box.dataset.online === "true") ||
      (which === "group" && box.dataset.group === group);
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
    ? `Todos los miembros pueden escribir, agregar a otros y cambiar el nombre. Hasta ${MAX_ROOM_MEMBERS} miembros.`
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
  es.addEventListener("reload", reloadAll);

  es.addEventListener("focus", () => window.focus());
}

// ---------- Inicio ----------

function bind() {
  $("search").addEventListener("input", renderContacts);
  $("settings-btn").addEventListener("click", openSettings);
  $("status-btn").addEventListener("click", openStatus);
  $("many-btn").addEventListener("click", openMany);
  $("new-room-btn").addEventListener("click", () => openRoomDialog("create"));
  $("room-add-btn").addEventListener("click", () => openRoomDialog("add"));
  $("room-rename-btn").addEventListener("click", () => openRoomDialog("rename"));
  $("room-leave-btn").addEventListener("click", leaveRoom);
  $("room-form").addEventListener("submit", saveRoom);
  $("delete-chat-btn").addEventListener("click", deleteChat);
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
    }
  });
  $("input").addEventListener("input", autoGrow);
  $("attach-btn").addEventListener("click", (e) =>
    state.mobile ? $("file-input").click() : pickAndSend(e.currentTarget, api.pickFiles));
  $("file-input").addEventListener("change", (e) => uploadPicked(e.currentTarget));
  $("folder-btn").addEventListener("click", (e) => pickAndSend(e.currentTarget, api.pickFolder));
  $("autostart-input").addEventListener("change", toggleAutostart);
  $("receipts-input").addEventListener("change", toggleReceipts);
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
  .catch((e) => showBanner(e.message));
