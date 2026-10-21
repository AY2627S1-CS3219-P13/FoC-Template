import {
  positiveInteger,
  encodeJSON,
  decodeJSON,
  expiryISO,
  newer,
  creationBody,
  creationKey,
} from "/orders/core.mjs";

const $ = (id) => document.getElementById(id);
const emptyDraft = () => ({
  pickup: "",
  delivery: "",
  items: [],
  offer: "",
  expiry: "",
  name: "",
  quantity: "1",
  description: "",
});
let admin = false;
let courierBusy = null, capacityVersion = 0, accountRevision = 0;
let user = null,
  epoch = 0,
  draft = emptyDraft(),
  attempt = null,
  pending = {},
  itemsBusy = false,
  posting = false;
let places = [],
  placesReady = false,
  view = "create",
  page = 1,
  listGeneration = 0,
  hasMore = false,
  selected = null;
let rows = [],
  cache = new Map(),
  listing = false,
  syncing = false,
  stale = true,
  detailStale = true,
  duplicateCandidate = null;
let lastListSync = 0;
let eventSource = null, lastEventPosition = 0n, listAgain = false;
const inFlight = new Set();
const time = (value) =>
  new Intl.DateTimeFormat("en-SG", {
    timeZone: "Asia/Singapore",
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value));
const node = (tag, text, className) => {
  const el = document.createElement(tag);
  if (text !== undefined) el.textContent = text;
  if (className) el.className = className;
  return el;
};
const show = (id, text, kind = "notice") => {
  const el = $(id);
  el.textContent = text;
  el.className = kind;
  el.hidden = !text;
};

async function api(path, options = {}) {
  const response = await fetch(`/api/v1/orders${path}`, {
    credentials: "same-origin",
    ...options,
    headers: { "Content-Type": "application/json", ...options.headers },
    signal: options.signal ?? AbortSignal.timeout(18000),
  });
  const data = decodeJSON(await response.text());
  if (!response.ok) {
    const error = new Error(
      data.error?.message || "The request could not be confirmed.",
    );
    error.code = data.error?.code;
    error.status = response.status;
    throw error;
  }
  return data;
}

function storageKey() {
  return `foc.order.v1.${user}`;
}
function persist(required = false) {
  if (!user) return;
  try {
    sessionStorage.setItem(
      storageKey(),
      JSON.stringify({ draft, attempt, pending }),
    );
  } catch {
    if (required)
      throw new Error(
        "Enable session storage before posting, so a lost response can be retried safely.",
      );
  }
}
function readDraft() {
  draft = emptyDraft();
  attempt = null;
  pending = {};
  try {
    const saved = JSON.parse(sessionStorage.getItem(storageKey()));
    if (saved && Array.isArray(saved.draft?.items)) {
      draft = { ...draft, ...saved.draft };
      attempt = saved.attempt || null;
      pending = saved.pending || {};
    }
  } catch {
    /* A corrupt local draft never becomes a server command. */
  }
  for (const [id, field] of [
    ["pickup", "pickup"],
    ["delivery", "delivery"],
    ["offer", "offer"],
    ["expiry", "expiry"],
    ["item-name", "name"],
    ["item-quantity", "quantity"],
    ["item-description", "description"],
  ])
    $(id).value = draft[field];
  renderItems();
  preview();
  controls();
  if (attempt)
    show(
      "form-message",
      "A previous post is unresolved. Retry to check the same attempt; your details are protected.",
      "warning",
    );
}
function clearSessionUI() {
  eventSource?.close();
  eventSource = null;
  lastEventPosition = 0n;
  show("event-notice", "");
  epoch++;
  user = null;
  admin = false;
  courierBusy = null;
  capacityVersion++;
  $("account-name").textContent = "";
  $("account-balance").textContent = "";
  places = [];
  placesReady = false;
  draft = emptyDraft();
  attempt = null;
  pending = {};
  rows = [];
  cache.clear();
  selected = null;
  inFlight.clear();
  posting = false;
  itemsBusy = false;
  duplicateCandidate = null;
  stale = true;
  detailStale = true;
  $("duplicate").hidden = true;
  $("create-form").reset();
  $("items").replaceChildren();
  $("orders").replaceChildren();
  $("order-detail").replaceChildren(
    node("p", "Sign in to view errands.", "muted"),
  );
  show("form-message", "");
  preview();
  controls();
}
async function session() {
  let capacityRevision = capacityVersion;
  try {
    const identity = await api("/session");
    if (identity.userId !== user) {
      clearSessionUI();
      user = identity.userId;
      capacityRevision = capacityVersion;
      readDraft();
      await loadPlaces();
    }
    admin = identity.roles.includes("admin");
    if (capacityRevision === capacityVersion)
      courierBusy = identity.courierBusy;
    void refreshAccount();
    renderDetail();
    $("disputes-tab").hidden = !admin;
    $("user-orders-link").hidden = !admin;
    show(
      "session-notice",
      "You’re signed in. Your session is checked for every request.",
    );
    controls();
    connectEvents();
    return true;
  } catch (err) {
    courierBusy = null;
    if (err.status === 401) clearSessionUI();
    show(
      "session-notice",
      err.status === 401
        ? "Log in through the existing Friend on Campus site, then return here. Your saved draft will be restored for that account."
        : "Cannot verify your session. Your draft is retained; retry when the service is available.",
      "warning",
    );
    stale = true;
    eventSource?.close();
    eventSource = null;
    controls();
    return false;
  }
}

async function refreshAccount() {
  const generation = epoch;
  const revision = ++accountRevision;
  const read = async (path) => {
    const response = await fetch(path, { credentials: "same-origin", signal: AbortSignal.timeout(2500) });
    if (!response.ok) throw new Error("Account unavailable");
    return decodeJSON(await response.text());
  };
  const [profile, wallet] = await Promise.allSettled([
    read("/api/v1/users/me"), read("/api/v1/wallets/me"),
  ]);
  if (generation !== epoch || revision !== accountRevision || !user) return;
  $("account-name").textContent = profile.status === "fulfilled" && profile.value.user?.id === user
    ? `${profile.value.user.displayName} · ${admin ? "Admin" : "User"}` : "Account unavailable";
  $("account-balance").textContent = wallet.status === "fulfilled" && wallet.value.userId === user
    ? `${wallet.value.available} credits available` : "Balance unavailable";
}

function statusPill(status) {
  return node("span", status, `pill status-${status.replaceAll(" ", "-")}`);
}

function connectEvents() {
  if (!user || eventSource) return;
  const generation = epoch;
  const source = new EventSource("/api/v1/orders/stream");
  eventSource = source;
  const current = () => generation === epoch && eventSource === source;
  source.addEventListener("snapshot", (event) => {
    if (!current()) return;
    if (decodeJSON(event.data).userId !== user) {
      clearSessionUI();
      void tick();
      return;
    }
    // Opening a connection is not evidence that the displayed list is current.
    listGeneration++;
    void loadList();
  });
  source.addEventListener("OrderCreated", (event) => {
    if (!current()) return;
    const position = BigInt(event.lastEventId);
    if (position <= lastEventPosition) return;
    lastEventPosition = position;
    show("event-notice", "A new errand is available. View Help a friend to see current requests.");
    // Events announce facts; the snapshot supplies current authorized details.
    // Do not insert an event's historical status over a newer confirmed state.
    if (view === "open") {
      listGeneration++;
      void loadList();
    }
  });
  let refreshQueued = false;
  const invalidate = (event) => {
    if (!current()) return;
    const position = BigInt(event.lastEventId);
    if (position <= lastEventPosition) return;
    lastEventPosition = position;
    if (refreshQueued) return;
    refreshQueued = true;
    // Coalesce a batch into one authoritative refresh. Existing snapshot
    // generation/version and request-age fences still decide what may render.
    queueMicrotask(() => {
      refreshQueued = false;
      if (!current()) return;
      listGeneration++;
      void loadList();
    });
  };
  source.addEventListener("OrderStatusChanged", invalidate);
  source.addEventListener("OrderDetailsChanged", invalidate);
  source.addEventListener("session_invalidated", () => {
    if (!current()) return;
    source.close();
    eventSource = null;
    stale = true;
    show("sync-notice", "List is not updated. Verifying your session…", "warning");
    void session();
  });
  source.onerror = () => {
    if (!current()) return;
    stale = true;
    show("sync-notice", "List is not updated. Reconnecting to live events…", "warning");
    renderDetail();
    if (source.readyState === EventSource.CLOSED) {
      // A rejected cursor or expired session cannot use native auto-reconnect.
      // The next validated session starts fresh and reconciles a snapshot.
      source.close();
      eventSource = null;
    }
  };
}

async function loadPlaces() {
  if (!user) return;
  const generation = epoch;
  placesReady = false;
  controls();
  $("reload-places").disabled = true;
  try {
    const loaded = [];
    for (let p = 1; ; p++) {
      const data = await api(`/places?page=${p}`);
      if (generation !== epoch) return;
      loaded.push(...data.places);
      if (!data.hasMore) break;
    }
    places = loaded;
    placesReady = true;
    for (const id of ["pickup", "delivery"]) {
      const select = $(id);
      select.replaceChildren(new Option(`Choose ${id}`, ""));
      for (const type of ["location", "supplier"]) {
        const group = node("optgroup");
        group.label =
          type === "location" ? "Campus locations" : "Active suppliers";
        for (const place of places.filter((p) => p.type === type))
          group.append(new Option(placeLabel(place), `${type}:${place.id}`));
        select.append(group);
      }
      if (draft[id] && !places.some((p) => `${p.type}:${p.id}` === draft[id]))
        select.append(
          new Option("Previous selection · no longer available", draft[id]),
        );
      select.value = draft[id];
    }
    preview();
  } catch (err) {
    if (generation === epoch) show("form-message", err.message, "error");
  } finally {
    if (generation === epoch) {
      $("reload-places").disabled = false;
      controls();
    }
  }
}
function placeLabel(p) {
  return p.type === "supplier" ? `Supplier: ${p.name} · ${p.locationName}` : `Campus: ${p.name}`;
}
function placeDetails(p) {
  return p
    ? [
        placeLabel(p),
        p.description,
        p.openingHours && `Hours: ${p.openingHours}`,
      ]
        .filter(Boolean)
        .join("\n")
    : "Choose a place to see its details.";
}
function preview() {
  for (const id of ["pickup", "delivery"]) {
    const p = places.find((p) => `${p.type}:${p.id}` === draft[id]);
    $(`preview-${id}`).textContent = p ? placeLabel(p) : `Choose a ${id}`;
    $(`${id}-detail`).textContent = placeDetails(p);
  }
  $("preview-count").textContent = String(draft.items.length);
  $("preview-offer").textContent = draft.offer || "—";
  try {
    $("preview-expiry").textContent = draft.expiry
      ? time(expiryISO(draft.expiry))
      : "Not set";
  } catch {
    $("preview-expiry").textContent = "Choose a valid date";
  }
}
function controls() {
  $("creation-fields").disabled = !user || !!attempt || posting;
  $("submit").disabled =
    !user || posting || itemsBusy || (!attempt && !placesReady);
  $("submit").textContent = posting
    ? "Confirming…"
    : attempt
      ? "Retry same attempt"
      : "Post errand";
  for (const id of ["add-item", "duplicate-proceed", "duplicate-decline"])
    $(id).disabled = itemsBusy;
  for (const id of ["item-name", "item-quantity", "item-description"])
    $(id).disabled = itemsBusy || !!duplicateCandidate;
  $("add-item").disabled = itemsBusy || !!duplicateCandidate;
  $("add-item").textContent = itemsBusy ? "Checking item…" : "+ Add item";
  $("pickup").disabled = !placesReady;
  $("delivery").disabled = !placesReady;
}
function renderItems() {
  $("items").replaceChildren();
  draft.items.forEach((item, index) => {
    const row = node("div", undefined, "item-row"),
      body = node("div");
    body.append(node("strong", `${item.quantity} × ${item.name}`));
    if (item.description) body.append(node("p", item.description));
    const remove = node("button", "Remove");
    remove.type = "button";
    remove.disabled = itemsBusy || !!duplicateCandidate;
    remove.setAttribute("aria-label", `Remove ${item.name}`);
    remove.addEventListener("click", () => {
      draft.items.splice(index, 1);
      persist();
      renderItems();
      preview();
    });
    row.append(body, remove);
    $("items").append(row);
  });
}
async function addItem(decision = "") {
  if (itemsBusy || !user) return;
  const generation = epoch;
  try {
    const item = duplicateCandidate || {
      name: $("item-name").value,
      description: $("item-description").value,
      quantity: $("item-quantity").value,
    };
    if (!item.name.trim()) throw new Error("Give the item a name.");
    const body = encodeJSON({
      existingItems: draft.items.map((i) => ({
        ...i,
        quantity: positiveInteger(i.quantity),
      })),
      item: { ...item, quantity: positiveInteger(item.quantity) },
      decision,
    });
    itemsBusy = true;
    controls();
    renderItems();
    const data = await api("/items/add", { method: "POST", body });
    if (generation !== epoch) return;
    if (data.requiresDecision) {
      duplicateCandidate = item;
      $("duplicate").hidden = false;
      $("duplicate-proceed").focus();
    } else {
      duplicateCandidate = null;
      $("duplicate").hidden = true;
      draft.items = data.items.map((i) => ({
        ...i,
        quantity: String(i.quantity),
      }));
      if (data.added) {
        draft.name = "";
        draft.description = "";
        draft.quantity = "1";
        $("item-name").value = "";
        $("item-description").value = "";
        $("item-quantity").value = "1";
      }
      show("form-message", "");
      persist();
    }
  } catch (err) {
    if (generation === epoch) show("form-message", err.message, "error");
  } finally {
    if (generation === epoch) {
      itemsBusy = false;
      renderItems();
      preview();
      controls();
    }
  }
}
async function post(event) {
  event.preventDefault();
  if (posting || !user || itemsBusy) return;
  const generation = epoch;
  try {
    if (!attempt) {
      const body = creationBody(draft);
      if (new Date(expiryISO(draft.expiry)).getTime() <= Date.now())
        throw new Error("Expiry must be in the future, in Singapore time.");
      attempt = { key: creationKey(), body };
    }
    persist(true);
    posting = true;
    controls();
    show(
      "form-message",
      "Posting… You can still browse other errands. Success will appear only after the server confirms it.",
    );
    const data = await api("", {
      method: "POST",
      headers: { "Idempotency-Key": attempt.key },
      body: attempt.body,
    });
    if (generation !== epoch) return;
    if (data.order.requesterId !== user) {
      await session();
      return;
    }
    const saved = data.order;
    attempt = null;
    draft = emptyDraft();
    persist();
    $("create-form").reset();
    readDraft();
    cache.set(saved.id, newer(cache.get(saved.id), saved));
    show(
      "form-message",
      `Errand confirmed · ${saved.status}. Find it in My requests.`,
      "success",
    );
  } catch (err) {
    if (generation !== epoch) return;
    const definitive =
      [400, 413, 415].includes(err.status) ||
      [
        "creation_abandoned",
        "insufficient_credits",
        "wallet_not_ready",
      ].includes(err.code);
    if (definitive) {
      attempt = null;
      persist();
    }
    show(
      "form-message",
      `${err.message || "The response was lost."}${attempt ? " The result is unresolved. Use Retry same attempt; do not post a replacement." : ""}`,
      attempt ? "warning" : "error",
    );
  } finally {
    if (generation === epoch) {
      posting = false;
      controls();
    }
  }
}

function remember(order) {
  const value = newer(cache.get(order.id), order);
  cache.set(value.id, value);
  return value;
}
function renderList() {
  $("orders").replaceChildren();
  if (!rows.length)
    $("orders").append(node("div", "No errands here yet.", "empty"));
  for (const record of rows) {
    const o = cache.get(record.id) || record;
    if (view === "open" && (o.status !== "open" || o.requesterId === user)) continue;
    const card = node(
        "button",
        undefined,
        `order-card${selected === o.id ? " selected" : ""}`,
      );
    card.append(
      node("h3", `${o.pickup.name} → ${o.delivery.name}`),
      node(
        "p",
        `${o.items.length} item${o.items.length === 1 ? "" : "s"} · ${o.offeredCredits} credits`,
      ),
      statusPill(o.status),
      node("small", `Expires ${time(o.expiresAt)} SGT`),
    );
    card.addEventListener("click", () => {
      selected = o.id;
      detailStale = true;
      renderList();
      renderDetail();
      void syncDetail();
    });
    $("orders").append(card);
  }
  $("pagination").hidden = view === "open";
  $("open-sort-label").hidden = view !== "open";
  $("request-filter-label").hidden = view !== "mine";
  $("page-label").textContent = `Page ${page}`;
  $("previous").disabled = page === 1;
  $("next").disabled = !hasMore;
}
async function loadList() {
  if (!user || view === "create") return;
  if (listing) {
    listAgain = true;
    return;
  }
  const generation = epoch,
    current = view,
    currentPage = page,
    revision = listGeneration,
    sort = $("open-sort").value,
    started = Date.now();
  listing = true;
  try {
    const filter = current === "mine" ? $("request-filter").value : "all";
    const data = await api(`?scope=${current}&page=${currentPage}&sort=${sort}&filter=${filter}`, {
      signal: AbortSignal.timeout(2500),
    });
    // Timers may resume late after a suspended tab; also fence the response age.
    if (Date.now() - started >= 2500) throw new Error("Refresh deadline exceeded.");
    if (generation !== epoch || view !== current || page !== currentPage || revision !== listGeneration)
      return;
    lastListSync = started;
    rows = data.orders.map(remember);
    hasMore = data.hasMore;
    stale = false;
    if (rows.some((o) => o.id === selected)) detailStale = false;
    show("sync-notice", "");
    renderList();
    renderDetail();
  } catch (err) {
    if (generation === epoch) {
      stale = true;
      show(
        "sync-notice",
        "List is not updated. Showing the last confirmed details; actions resume after synchronization.",
        "warning",
      );
      renderDetail();
    }
  } finally {
    listing = false;
    if (listAgain) {
      listAgain = false;
      void loadList();
    }
  }
}
function renderDetail() {
  const o = cache.get(selected);
  const detail = $("order-detail");
  if (!o) {
    detail.replaceChildren(node("h3", "Select an errand"), node("p", "See the exact pickup, delivery and item details here.", "muted"));
    return;
  }
  detail.replaceChildren(
    node("p", "Errand details", "eyebrow"),
    node("h3", `${o.pickup.name} → ${o.delivery.name}`),
    statusPill(o.status),
  );
  for (const [label, text] of [
    ["Pickup", placeDetails(o.pickup)],
    ["Delivery", placeDetails(o.delivery)],
    [
      "Items",
      o.items
        .map(
          (i) =>
            `${i.quantity} × ${i.name}${i.description ? `\n${i.description}` : ""}`,
        )
        .join("\n\n"),
    ],
    ["Offer", `${o.offeredCredits} credits`],
    ["Expiry · Singapore time", `${time(o.expiresAt)} SGT`],
  ])
    detail.append(node("h4", label), node("p", text));
  if (o.cancellationReason)
    detail.append(node("p", o.cancellationReason, "warning"));
  if (o.status === "delivered")
    detail.append(
      node(
        "p",
        "Delivered. The requester can confirm or raise a dispute. Without a dispute, completion is automatic five minutes after delivery. Escrow remains reserved until completion.",
        "notice",
      ),
    );
  const pendingAction = pending[o.id];
  if (pendingAction)
    detail.append(
      node(
        "p",
        inFlight.has(o.id)
          ? "Updating… Your last confirmed status is shown above."
          : "The previous action is unresolved. Retry the same action to confirm its result.",
        "warning",
      ),
    );
  const actions = node("div", undefined, "actions");
  if (o.completionRequestedAt && o.status === "delivered")
    detail.append(node("p", "Updating… Confirmation is pending. A dispute can still be raised before completion.", "notice"));
  let available = [];
  if (o.status === "open" && o.requesterId !== user) available = ["accept"];
  if (o.courierId === user && o.status === "accepted") available = ["pick_up"];
  if (o.courierId === user && o.status === "picked up") available = ["deliver"];
  if (o.requesterId === user && ["open", "accepted"].includes(o.status)) available = ["cancel"];
  if (o.requesterId === user && o.status === "delivered") available = o.completionRequestedAt ? ["dispute"] : ["confirm", "dispute"];
  if (admin && o.status === "dispute") available = ["resolve_completed", "resolve_not_completed"];
  if (pendingAction) available = [pendingAction.action];
  for (const action of available) {
    const button = node("button", pendingAction && !inFlight.has(o.id) ? "Retry same action" : {
      accept: "Accept errand", pick_up: "Mark picked up", deliver: "Mark delivered",
      cancel: "Cancel errand", confirm: "Confirm delivery", dispute: "Raise dispute",
      resolve_completed: "Resolve as completed", resolve_not_completed: "Resolve as not completed",
    }[action], "primary");
    button.disabled = stale || detailStale || inFlight.has(o.id) ||
      (action === "accept" && courierBusy !== false && !pendingAction) ||
      (action === "accept" && Object.keys(pending).some((id) => id !== o.id));
    button.addEventListener("click", () => void act(o.id, action));
    actions.append(button);
    if (action === "accept" && courierBusy !== false && !pendingAction)
      actions.append(node("span", courierBusy ? "You have an ongoing order." : "Checking your delivery availability…", "muted"));
  }
  if (pendingAction?.message)
    detail.append(node("p", pendingAction.message, "error"));
  detail.append(actions);
}
async function act(id, action) {
  if (inFlight.has(id) || !user) return;
  const generation = epoch;
  try {
    pending[id] = { action };
    persist(true);
    inFlight.add(id);
    renderDetail();
    const data = await api(`/${encodeURIComponent(id)}/actions`, {
      method: "POST",
      body: JSON.stringify({ action }),
    });
    if (generation !== epoch) return;
    remember(data.order);
    if (["accept", "pick_up", "deliver"].includes(action)) {
      capacityVersion++;
      courierBusy = null;
      renderDetail();
      await session();
      if (generation !== epoch) return;
    }
    void refreshAccount();
    delete pending[id];
    persist();
    listGeneration++;
    detailStale = false;
  } catch (err) {
    if (generation !== epoch) return;
    if (err.status && err.status < 500) {
      delete pending[id];
      show("sync-notice", err.message, "warning");
    } else
      pending[id] = {
        action,
        message:
          "The result could not be confirmed. Retry this action before starting a conflicting action.",
      };
    persist();
  } finally {
    inFlight.delete(id);
    if (generation === epoch) {
      renderList();
      renderDetail();
      void loadList();
    }
  }
}
async function syncDetail() {
  if (!selected || !user || syncing || inFlight.has(selected)) return;
  // The list already reconciles every displayed row. Avoid a second competing
  // admission and catalogue lookup for the same selected order.
  if (rows.some((o) => o.id === selected)) return;
  const generation = epoch,
    id = selected;
  syncing = true;
  try {
    // This signed-in refresh detects Supplier changes without retaining a cookie
    // server-side. Facts and refund intent commit together before the response.
    const data = await api(`/${encodeURIComponent(id)}/refresh`, {
      method: "POST",
      body: "{}",
      signal: AbortSignal.timeout(2500),
    });
    if (generation !== epoch) return;
    remember(data.order);
    if (selected === id) detailStale = false;
    renderList();
    renderDetail();
  } catch (err) {
    if (generation !== epoch) return;
    detailStale = true;
    show(
      "sync-notice",
      `List is not updated. ${err.message || "Live refresh is unavailable."} Last confirmed details are retained.`,
      "warning",
    );
    if (err.status === 404) {
      cache.delete(id);
      if (selected === id) {
        selected = null;
        $("order-detail").replaceChildren(
          node("p", "This errand is no longer available to you.", "muted"),
        );
      }
    }
    renderDetail();
  } finally {
    syncing = false;
  }
}
function setView(next) {
  view = next;
  selected = null;
  detailStale = true;
  renderDetail();
  listGeneration++;
  stale = true;
  show("sync-notice", "List is not updated. Synchronizing…", "warning");
  page = 1;
  rows = [];
  document.querySelectorAll("[data-view]").forEach((button) => {
    if (button.dataset.view === view)
      button.setAttribute("aria-current", "page");
    else button.removeAttribute("aria-current");
  });
  $("create-view").hidden = view !== "create";
  $("list-view").hidden = view === "create";
  $("list-label").textContent =
    {
      mine: "Your posted errands",
      open: "Open errands from other students",
      courier: "Your current and previous deliveries",
      disputes: "Disputes awaiting administrator review",
    }[view] || "";
  if (view !== "create") {
    renderList();
    void loadList();
  }
}
for (const [id, key] of [
  ["pickup", "pickup"],
  ["delivery", "delivery"],
  ["offer", "offer"],
  ["expiry", "expiry"],
  ["item-name", "name"],
  ["item-quantity", "quantity"],
  ["item-description", "description"],
])
  $(id).addEventListener("input", () => {
    draft[key] = $(id).value;
    persist();
    preview();
  });
$("create-form").addEventListener("submit", post);
$("add-item").addEventListener("click", () => void addItem());
$("duplicate-proceed").addEventListener("click", () => void addItem("proceed"));
$("duplicate-decline").addEventListener(
  "click",
  () => void addItem("do_not_proceed"),
);
$("reload-places").addEventListener("click", () => void loadPlaces());
$("reload-list").addEventListener("click", () => {
  void loadList();
  void syncDetail();
});
document
  .querySelectorAll("[data-view]")
  .forEach((button) =>
    button.addEventListener("click", () => setView(button.dataset.view)),
  );
$("previous").addEventListener("click", () => {
  page--;
  void loadList();
});
$("next").addEventListener("click", () => {
  page++;
  void loadList();
});
let ticking = false;
async function tick() {
  if (document.hidden) return;
  // Each list request validates its own session. A slow session/catalogue
  // editor request must not hold up the independent two-second refresh clock.
  if (user) {
    void loadList();
    if (view !== "create") void syncDetail();
  }
  if (ticking) return;
  ticking = true;
  try {
    const hadUser = !!user;
    if (await session() && !hadUser) {
      void loadList();
    }
  } finally {
    ticking = false;
  }
}
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) void tick();
});
window.addEventListener("focus", () => void tick());
void tick();
setInterval(() => void tick(), 2000);
$("open-sort").addEventListener("change", () => {
  listGeneration++;
  void loadList();
});
$("request-filter").addEventListener("change", () => setView("mine"));
window.addEventListener("offline", () => {
  stale = true;
  show("sync-notice", "List is not updated", "warning");
  renderDetail();
});
window.addEventListener("online", () => void tick());

setInterval(() => {
  if (view !== "create" && Date.now() - lastListSync >= 4500) {
    stale = true;
    show("sync-notice", "List is not updated", "warning");
    renderDetail();
  }
}, 500);
