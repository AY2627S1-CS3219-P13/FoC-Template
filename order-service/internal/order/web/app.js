import { createSuggestions } from "/orders/pricing.mjs";
import {
  positiveInteger,
  encodeJSON,
  decodeJSON,
  expiryISO,
  newer,
  creationBody,
  creationKey,
  newestFirst,
  scheduledHistoryPage,
  distanceMeters,
  pickupLocationId,
  formatDistance,
  openComparator,
  alertState,
  alertScope,
  alertCoverage,
  base64UrlBytes,
  subscriptionBody,
  deepLink,
} from "/orders/core.mjs";

// The server rotates the event stream every 8s, so a stream error alone is not
// staleness; the list is stale only when no list sync landed within this window.
const STALE_AFTER_MS = 4500;
const POLL_INTERVAL_MS = 2000;
const STALE_CHECK_INTERVAL_MS = 500;
const scheduleMode = location.pathname === "/orders/schedule";
// Imported orders retain assignment history in every later state. Unassigned
// scheduled IDs, including cancelled/expired requests, remain Schedule-owned.
const scheduleOwned = order => order?.id?.startsWith("scheduled-") && !order.courierId;

const $ = (id) => document.getElementById(id);
const emptyDraft = () => ({
  pickup: "",
  delivery: "",
  items: [],
  offer: "",
  expiry: "",
  date: "", slot: "",
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

const suggestions = createSuggestions({
  request: (body, key, signal) => api("/price-quotes", {
    method: "POST", body: JSON.stringify(body), headers: {"Idempotency-Key": key}, signal,
  }),
  render({state, quote, sessionExpired}) {
    $("pricing-suggestion").hidden = scheduleMode;
    $("use-suggestion").disabled = state !== "ready" || !!attempt || posting || !user;
    $("pricing-message").textContent = state === "ready"
      ? `${quote.recommendedCredits} credits · fresh until ${time(quote.validUntil)} SGT.`
      : state === "loading" ? "Calculating suggestion…"
      : state === "unavailable" ? "Suggestion unavailable. We’ll retry automatically. You can enter your own offered credits."
      : "Choose pickup and delivery to see a suggestion.";
    if (sessionExpired) clearSessionUI();
  },
});
function updateSuggestion() {
  if (scheduleMode || !user || !placesReady || attempt || posting || document.hidden || view !== "create") { suggestions.update(null); return; }
  try {
    const place = value => {
      const selected = places.find(p => `${p.type}:${p.id}` === value);
      if (!selected) throw new Error("Select a place");
      return {type: selected.type, id: selected.id};
    };
    const pickup = place(draft.pickup), delivery = place(draft.delivery);
    if (draft.pickup === draft.delivery) throw new Error("Choose different places");
    const total = draft.items.reduce((sum, item) => sum + positiveInteger(item.quantity), 0n);
    if (total > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error("Quantity exceeds quote range");
    suggestions.update({pickup, delivery, ...(total > 0n ? {totalItemQuantity: Number(total)} : {})});
  } catch { suggestions.update(null); }
}
$("use-suggestion").addEventListener("click", () => {
  const quote = suggestions.current();
  if (!quote || !user || attempt || posting) return;
  draft.offer = String(quote.recommendedCredits);
  $("offer").value = draft.offer;
  persist(); preview();
});

async function api(path, options = {}) {
  const response = await fetch(`${options.schedule ? "/api/v1/scheduled-orders" : "/api/v1/orders"}${path}`, {
    credentials: "same-origin",
    ...options,
    headers: { "Content-Type": "application/json", ...options.headers },
    signal: options.signal ?? AbortSignal.timeout(18000),
  });
  // 204 No Content (alert subscription changes) has an empty body.
  const text = await response.text();
  const data = text ? decodeJSON(text) : {};
  if (!response.ok) {
    const error = new Error(
      data.error?.message || "The request could not be confirmed.",
    );
    error.code = data.error?.code;
    error.status = response.status;
    const retryAfter = Number(response.headers.get("Retry-After"));
    if (Number.isFinite(retryAfter) && retryAfter > 0 && retryAfter <= 3600) error.retryAfterMs = retryAfter * 1000;
    throw error;
  }
  return data;
}

function storageKey() {
  return `foc.order.v1.${scheduleMode ? "scheduled." : ""}${user}`;
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
  suggestions.reset();
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
  if (scheduleMode) { $("schedule-date").value=draft.date; void loadSlots(); }
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
  suggestions.reset();
  document.querySelectorAll(".action-dialog").forEach(dialog => dialog.close());
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
  alerts = noAlerts(alerts.revision + 1);
  $("alerts-panel").hidden = true;
  $("alerts-location").replaceChildren();
  alertMessage("");
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
    $("courier-capacity").textContent=`${identity.courierActive} / ${identity.courierLimit} active deliveries`;
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
    // CONNECTING is the native reconnect after a planned rotation. Staleness is
    // reported by the freshness watchdog below, not by the stream state.
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
    fillStartingLocations();
    fillAlertLocations();
    renderAlerts();
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
// The courier's starting point is a per-browser convenience, kept across reloads.
const FROM_KEY = "foc-orders-start-from";
function savedStart() {
  try {
    return localStorage.getItem(FROM_KEY) || "";
  } catch {
    return "";
  }
}
function saveStart(value) {
  try {
    if (value) localStorage.setItem(FROM_KEY, value);
    else localStorage.removeItem(FROM_KEY);
  } catch {
    // Storage may be blocked; the selection still applies to this page.
  }
}
const placeable = (p) => p.type === "location" && typeof p.latitude === "number" && typeof p.longitude === "number";
function startingLocation() {
  const id = $("open-from").value;
  return id ? places.find((p) => placeable(p) && p.id === id) || null : null;
}
function fillStartingLocations() {
  const select = $("open-from"),
    wanted = select.value || savedStart();
  select.replaceChildren(new Option("No starting location", ""));
  for (const place of places.filter(placeable)) select.append(new Option(place.name, place.id));
  select.value = places.some((p) => placeable(p) && p.id === wanted) ? wanted : "";
  startingLocationChanged();
  // Show restored distance labels now; the next poll also sends `from`.
  if (view === "open") renderList();
}
// Nearest pickup needs a starting point; fall back to expiry when it is cleared.
function startingLocationChanged() {
  const hasStart = Boolean(startingLocation());
  $("open-sort").querySelector('option[value="distance"]').disabled = !hasStart;
  if (!hasStart && $("open-sort").value === "distance") $("open-sort").value = "expiry";
}
function pickupDistance(order) {
  const start = startingLocation();
  const pickup = start && places.find((p) => placeable(p) && p.id === pickupLocationId(order));
  return pickup ? distanceMeters(start, pickup) : null;
}
// Nearby alerts (Web Push). The server keeps one preference per account; each
// browser keeps its own push subscription, registered with the server.
const pushSupported = () => "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
const noAlerts = (revision) => ({ loaded: false, busy: false, pushAvailable: false, key: null, preference: null, devices: 0, subscription: null, locationTouched: false, radiusTouched: false, revision });
let alerts = noAlerts(0);
async function alertRegistration(register = false) {
  if (!register) return navigator.serviceWorker.getRegistration("/orders/");
  const registration = await navigator.serviceWorker.register("/orders/sw.js", { scope: "/orders/" });
  // This page is outside the worker's scope, so wait for activation directly.
  const worker = registration.installing || registration.waiting;
  if (!registration.active && worker)
    await new Promise((resolve, reject) => {
      worker.addEventListener("statechange", () => {
        if (worker.state === "activated") resolve();
        if (worker.state === "redundant") reject(new Error("The alert service could not start in this browser."));
      });
    });
  return registration;
}
async function loadAlerts() {
  if (!user) return;
  const generation = epoch,
    revision = ++alerts.revision;
  try {
    const data = await api("/alerts");
    let subscription = null;
    if (pushSupported() && window.isSecureContext) subscription = (await (await alertRegistration())?.pushManager.getSubscription()) || null;
    if (generation !== epoch || revision !== alerts.revision) return;
    alerts = { ...alerts, loaded: true, pushAvailable: data.pushAvailable, key: data.vapidPublicKey, preference: data.preference, devices: data.devices, subscription };
    if (!alerts.busy) alertMessage("");
  } catch (err) {
    if (generation !== epoch || revision !== alerts.revision) return;
    alerts.loaded = false;
    alertMessage(err.status === 401 ? "" : "Alert settings could not be loaded. Use Refresh to try again.");
  }
  renderAlerts();
}
function alertMessage(text) {
  $("alerts-message").textContent = text;
  $("alerts-message").hidden = !text;
}
function fillAlertLocations() {
  const select = $("alerts-location"),
    wanted = (!alerts.locationTouched && alerts.preference?.locationId) || select.value || $("open-from").value;
  select.replaceChildren(new Option("Choose a location", ""));
  for (const place of places.filter(placeable)) select.append(new Option(place.name, place.id));
  select.value = places.some((p) => placeable(p) && p.id === wanted) ? wanted : "";
}
function renderAlerts() {
  $("alerts-panel").hidden = view !== "open" || !user;
  const preference = alerts.preference;
  const state = alertState({
    supported: pushSupported(),
    secure: window.isSecureContext,
    permission: "Notification" in window ? Notification.permission : "default",
    pushAvailable: alerts.pushAvailable,
    preference,
    subscribed: Boolean(alerts.subscription),
    devices: alerts.devices,
  });
  const where = preference && places.find((p) => p.type === "location" && p.id === preference.locationId);
  $("alerts-state").textContent = !alerts.loaded
    ? "Checking alert settings…"
    : state.key === "on" && where
      ? `On for this device · ${alertScope(preference.radiusMeters, where.name)}`
      : state.message;
  $("alerts-panel").dataset.state = alerts.loaded ? state.key : "loading";
  if (preference && !alerts.locationTouched && $("alerts-location").value !== preference.locationId) fillAlertLocations();
  if (preference && !alerts.radiusTouched) $("alerts-radius").value = String(preference.radiusMeters);
  const usable = alerts.loaded && ["on", "off", "elsewhere"].includes(state.key);
  $("alerts-location").disabled = $("alerts-radius").disabled = !usable || alerts.busy;
  $("alerts-enable").hidden = state.key === "on";
  $("alerts-enable").disabled = !usable || alerts.busy || !$("alerts-location").value;
  $("alerts-save").hidden = $("alerts-disable").hidden = state.key !== "on";
  $("alerts-save").disabled = alerts.busy || !$("alerts-location").value;
  $("alerts-disable").disabled = alerts.busy;
  renderCoverage(usable);
}
// Lists the campus locations the chosen location and distance would cover.
function renderCoverage(usable) {
  const covered = usable ? alertCoverage(places, $("alerts-location").value, Number($("alerts-radius").value)) : [];
  $("alerts-coverage").hidden = !covered.length;
  if (!covered.length) return;
  const total = places.filter(placeable).length;
  $("alerts-coverage-title").textContent = `New errands with a pickup at these places will alert you (${covered.length} of ${total} campus locations):`;
  $("alerts-coverage-list").replaceChildren(
    ...covered.map((place, i) => {
      const item = document.createElement("li");
      const where = i === 0 ? "your location" : formatDistance(place.distance) || "under 50 m";
      const suppliers = place.suppliers ? ` · ${place.suppliers} supplier${place.suppliers === 1 ? "" : "s"}` : "";
      item.textContent = `${place.name} · ${where}${suppliers}`;
      return item;
    }),
  );
}
async function savePreference(enabled) {
  const data = await api("/alerts", {
    method: "PUT",
    body: JSON.stringify({ enabled, locationId: $("alerts-location").value, radiusMeters: Number($("alerts-radius").value) }),
  });
  alerts.preference = data.preference;
  alerts.locationTouched = alerts.radiusTouched = false;
}
async function alertAction(work) {
  if (alerts.busy) return;
  const generation = epoch;
  alerts.busy = true;
  alertMessage("");
  renderAlerts();
  let failure = "";
  try {
    await work();
  } catch (err) {
    failure = err.message || "Alerts could not be updated. Try again.";
  } finally {
    alerts.busy = false;
  }
  if (generation !== epoch) return;
  await loadAlerts();
  alertMessage(failure);
}
async function enableAlerts() {
  if ((await Notification.requestPermission()) !== "granted")
    throw new Error("Notifications were not allowed, so alerts stay off. Allow them for this site to turn alerts on.");
  const registration = await alertRegistration(true);
  const options = { userVisibleOnly: true, applicationServerKey: base64UrlBytes(alerts.key) };
  let subscription;
  try {
    subscription = await registration.pushManager.subscribe(options);
  } catch (err) {
    // A subscription made with an older server key must be replaced.
    const existing = await registration.pushManager.getSubscription();
    if (!existing) throw err;
    await existing.unsubscribe();
    subscription = await registration.pushManager.subscribe(options);
  }
  await api("/alerts/subscription", { method: "PUT", body: JSON.stringify(subscriptionBody(subscription.toJSON())) });
  await savePreference(true);
}
async function disableAlerts() {
  const subscription = alerts.subscription;
  if (subscription) {
    await api("/alerts/subscription", { method: "DELETE", body: JSON.stringify({ endpoint: subscription.endpoint }) });
    await subscription.unsubscribe();
  }
  // With no device left, also switch the account preference off.
  if (alerts.devices <= 1) await savePreference(false);
}
// Open Help a friend with one errand selected: a notification link or click.
function openErrand(id) {
  if (view !== "open") setView("open");
  if (id) {
    selected = id;
    detailStale = true;
    renderList();
    renderDetail();
    void syncDetail();
  }
}
const pendingLink = deepLink(location.search);
if (pendingLink) history.replaceState(null, "", location.pathname);
if ("serviceWorker" in navigator)
  navigator.serviceWorker.addEventListener("message", (event) => {
    if (event.origin !== location.origin || event.data?.type !== "open-errand" || !user) return;
    openErrand(typeof event.data.orderId === "string" ? event.data.orderId : null);
  });
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
  updateSuggestion();
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
  if(scheduleMode) $("preview-expiry").textContent=draft.slot ? `${time(draft.slot)} – ${time(new Date(Date.parse(draft.slot)+1800000))} SGT` : "Choose a delivery window";
}
function controls() {
  $("creation-fields").disabled = !user || !!attempt || posting;
  $("use-suggestion").disabled = !user || !!attempt || posting || !suggestions.current();
  if (attempt || posting) updateSuggestion();
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
      if (scheduleMode && !draft.slot) throw new Error("Choose an available delivery window.");
      const body = creationBody(draft, scheduleMode ? draft.slot : undefined);
      if (!scheduleMode && new Date(expiryISO(draft.expiry)).getTime() <= Date.now())
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
      schedule: scheduleMode,
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
    if (view === "open" && (!["open","scheduled"].includes(o.status) || o.requesterId === user)) continue;
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
    if (o.windowStart) card.append(node("p",`Delivery ${time(o.windowStart)} – ${time(o.expiresAt)} SGT`));
    const distance = view === "open" ? pickupDistance(o) : null;
    if (distance !== null) {
      const away = formatDistance(distance), start = startingLocation().name;
      card.append(node("small", away ? `Approximately ${away} from ${start}` : `Pickup at ${start}`, "distance"));
    }
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
  $("open-from-label").hidden = view !== "open";
  $("alerts-panel").hidden = view !== "open" || !user;
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
    from = current === "open" ? $("open-from").value : "",
    started = Date.now();
  listing = true;
  try {
    const filter = current === "mine" ? $("request-filter").value : "all";
    const start = from ? `&from=${encodeURIComponent(from)}` : "";
    const data = await api(`?scope=${current}&page=${currentPage}&sort=${sort}&filter=${filter}${start}`, {
      signal: AbortSignal.timeout(2500),
    });
    if (current === "mine" || (current === "open" && currentPage === 1)) {
      const scheduled=await api(`?scope=${current}`,{schedule:true,signal:AbortSignal.timeout(Math.max(1,2500-(Date.now()-started)))});
      const history = current === "mine" ? scheduledHistoryPage(scheduled.orders, currentPage, data.pageStart, data.nextPageStart) : scheduled.orders;
      data.orders.push(...history.filter(o=>filter==="all" || (filter==="past") === ["cancelled","expired"].includes(o.status)));
    }
    // Timers may resume late after a suspended tab; also fence the response age.
    if (Date.now() - started >= 2500) throw new Error("Refresh deadline exceeded.");
    if (generation !== epoch || view !== current || page !== currentPage || revision !== listGeneration)
      return;
    lastListSync = started;
    rows = [...new Map(data.orders.map(o=>[o.id,remember(o)])).values()];
    if(current==="mine") rows.sort(newestFirst);
    // Re-sort after merging scheduled requests, which Order Service did not rank.
    if(current==="open") rows.sort(openComparator(sort, pickupDistance));
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
  for (const [label, text] of [["Dispute description", o.disputeDescription], ["Admin decision explanation", o.resolutionExplanation]]) {
    if (text) detail.append(node("h4", label), node("p", text));
  }
  for (const [label, photo] of [["Proof of delivery", o.deliveryPhoto], ["Dispute photo", o.disputePhoto]]) {
    if (!photo) continue;
    const img = node("img");
    img.src = photo;
    img.alt = label;
    img.className = "evidence-photo";
    detail.append(node("h4", label), img);
  }
  if (o.windowStart) detail.append(node("h4","Fixed delivery window"),node("p",`${time(o.windowStart)} – ${time(o.expiresAt)} SGT`));
  if (o.transferPending) detail.append(node("p","Acceptance pending. Waiting for confirmed assignment.","notice"));
  if (o.refundPending) detail.append(node("p","Credit refund pending.","notice"));
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
  if (["open","scheduled"].includes(o.status) && o.requesterId !== user) available = ["accept"];
  if (o.courierId === user && o.status === "accepted") available = ["pick_up"];
  if (o.courierId === user && o.status === "picked up") available = ["deliver"];
  if (o.requesterId === user && ["open", "scheduled", "accepted"].includes(o.status)) available = ["cancel"];
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
      o.transferPending || (action === "deliver" && o.windowStart && (Date.now()<Date.parse(o.windowStart) || Date.now()>=Date.parse(o.expiresAt))) ||
      (action === "accept" && courierBusy !== false && !pendingAction) ||
      (action === "accept" && Object.keys(pending).some((id) => id !== o.id));
    button.addEventListener("click", () => {
      if (!pendingAction && ["deliver", "dispute", "resolve_completed", "resolve_not_completed"].includes(action)) openActionForm(o.id, action);
      else void act(o.id, action);
    });
    actions.append(button);
    if (action === "accept" && courierBusy !== false && !pendingAction)
      actions.append(node("span", courierBusy ? "You have three active orders." : "Checking your delivery availability…", "muted"));
  }
  if (pendingAction?.message)
    detail.append(node("p", pendingAction.message, "error"));
  detail.append(actions);
}
async function photoData(file) {
  if (!file) return "";
  if (!["image/jpeg", "image/png", "image/webp"].includes(file.type) || file.size > 20 * 1024 * 1024)
    throw new Error("Choose a JPEG, PNG or WebP image up to 20 MiB.");
  const bitmap = await createImageBitmap(file);
  try {
    const scale = Math.min(1, 1600 / Math.max(bitmap.width, bitmap.height));
    const canvas = document.createElement("canvas");
    canvas.width = Math.max(1, Math.round(bitmap.width * scale));
    canvas.height = Math.max(1, Math.round(bitmap.height * scale));
    const context = canvas.getContext("2d");
    context.fillStyle = "white";
    context.fillRect(0, 0, canvas.width, canvas.height);
    context.drawImage(bitmap, 0, 0, canvas.width, canvas.height);
    for (const quality of [0.85, 0.7, 0.5, 0.3]) {
      const data = canvas.toDataURL("image/jpeg", quality);
      if (data.length - data.indexOf(",") - 1 <= 4 * Math.ceil(512 * 1024 / 3)) return data;
    }
    throw new Error("This photo is too large. Choose a smaller photo.");
  } finally {
    bitmap.close();
  }
}
function openActionForm(id, action) {
  const generation = epoch;
  const dialog = node("dialog", undefined, "action-dialog");
  const form = node("form");
  const resolving = action.startsWith("resolve_");
  const title = action === "deliver" ? "Mark delivered" : action === "dispute" ? "Raise dispute" : action === "resolve_completed" ? "Resolve as completed" : "Resolve as not completed";
  dialog.setAttribute("aria-label", title);
  form.append(node("h3", title));
  let text, photo;
  if (action !== "deliver") {
    const label = node("label", resolving ? "Decision explanation (required)" : "Dispute description (required)");
    text = node("textarea");
    text.required = true;
    text.maxLength = 2000;
    text.rows = 5;
    label.append(text);
    form.append(label);
  }
  if (!resolving) {
    const label = node("label", action === "deliver" ? "Proof of delivery photo (optional)" : "Dispute photo (optional)");
    photo = node("input");
    photo.type = "file";
    photo.accept = "image/jpeg,image/png,image/webp";
    label.append(photo);
    form.append(label, node("p", "JPEG, PNG or WebP, up to 20 MiB. Photos are resized before submission.", "muted"));
  }
  if (action === "dispute") form.append(node("p", "Your description and photo cannot be edited after submission.", "notice"));
  const error = node("p", "", "error");
  error.setAttribute("role", "alert");
  const buttons = node("div", undefined, "actions");
  const cancel = node("button", "Cancel");
  cancel.type = "button";
  cancel.addEventListener("click", () => dialog.close());
  const submit = node("button", "Submit", "primary");
  submit.type = "submit";
  buttons.append(cancel, submit);
  form.append(error, buttons);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    error.textContent = "";
    if (text && !text.value.trim()) {
      error.textContent = "Enter an explanation before submitting.";
      text.focus();
      return;
    }
    submit.disabled = true;
    try {
      const payload = { action };
      if (text) payload[resolving ? "explanation" : "description"] = text.value.trim();
      if (photo?.files[0]) payload.photo = await photoData(photo.files[0]);
      if (!dialog.open || generation !== epoch) return;
      dialog.close();
      await act(id, action, payload);
    } catch (err) {
      error.textContent = err.message || "Unable to read this photo. Choose another image.";
    } finally {
      submit.disabled = false;
    }
  });
  dialog.append(form);
  dialog.addEventListener("close", () => dialog.remove(), { once: true });
  document.body.append(dialog);
  dialog.showModal();
}
async function act(id, action, payload = pending[id]?.payload || { action }) {
  if (inFlight.has(id) || !user) return;
  const generation = epoch;
  try {
    pending[id] = { action, payload };
    persist(true);
    inFlight.add(id);
    renderDetail();
    const data = await api(`/${encodeURIComponent(id)}/actions`, {
      schedule: scheduleOwned(cache.get(id)),
      method: "POST",
      body: JSON.stringify(payload),
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
        payload,
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
    const scheduled=scheduleOwned(cache.get(id));
    const data = await api(`/${encodeURIComponent(id)}${scheduled ? "" : "/refresh"}`, {
      schedule: scheduled,
      method: scheduled ? "GET" : "POST",
      ...(scheduled ? {} : {body: "{}"}),
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
function updateActiveTab() {
  const scheduling = scheduleMode && view === "create";
  if (scheduling) $("schedule-order-link").setAttribute("aria-current", "page");
  else $("schedule-order-link").removeAttribute("aria-current");
  document.querySelectorAll("[data-view]").forEach((button) => {
    if (!scheduling && button.dataset.view === view)
      button.setAttribute("aria-current", "page");
    else button.removeAttribute("aria-current");
  });
}
function setView(next) {
  if (scheduleMode && next === "create") {
    location.assign("/orders");
    return;
  }
  view = next;
  updateSuggestion();
  selected = null;
  detailStale = true;
  renderDetail();
  listGeneration++;
  stale = true;
  show("sync-notice", "List is not updated. Synchronizing…", "warning");
  page = 1;
  rows = [];
  updateActiveTab();
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
  if (view === "open") void loadAlerts();
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
  if (view === "open") void loadAlerts();
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
async function checkAlertDevice() {
  if (view !== "open" || !alerts.loaded || alerts.busy || !alerts.subscription) return;
  const known = alerts.subscription.endpoint;
  try {
    const current = await (await alertRegistration())?.pushManager.getSubscription();
    if (current?.endpoint !== known && alerts.subscription?.endpoint === known) await loadAlerts();
  } catch {
    // Unavailable now; the next tick checks again.
  }
}
async function tick() {
  if (document.hidden) return;
  // Each list request validates its own session. A slow session/catalogue
  // editor request must not hold up the independent two-second refresh clock.
  if (user) {
    void loadList();
    if (view !== "create") void syncDetail();
    void checkAlertDevice();
  }
  if (ticking) return;
  ticking = true;
  try {
    const hadUser = !!user;
    if (await session() && !hadUser) {
      if (pendingLink && view !== "open") openErrand(pendingLink.order);
      else if (view === "open") void loadAlerts();
      void loadList();
    }
  } finally {
    ticking = false;
  }
}
document.addEventListener("visibilitychange", () => {
  updateSuggestion();
  if (!document.hidden) void tick();
});
window.addEventListener("focus", () => void tick());
void tick();
setInterval(() => void tick(), POLL_INTERVAL_MS);
$("open-sort").addEventListener("change", () => {
  listGeneration++;
  void loadList();
});
$("open-from").addEventListener("change", () => {
  saveStart($("open-from").value);
  startingLocationChanged();
  listGeneration++;
  void loadList();
});
$("request-filter").addEventListener("change", () => setView("mine"));
$("alerts-location").addEventListener("change", () => {
  alerts.locationTouched = true;
  renderAlerts();
});
$("alerts-radius").addEventListener("change", () => {
  alerts.radiusTouched = true;
  renderAlerts();
});
$("alerts-enable").addEventListener("click", () => void alertAction(enableAlerts));
$("alerts-save").addEventListener("click", () => void alertAction(() => savePreference(true)));
$("alerts-disable").addEventListener("click", () => void alertAction(disableAlerts));
window.addEventListener("offline", () => {
  stale = true;
  show("sync-notice", "List is not updated", "warning");
  renderDetail();
});
window.addEventListener("online", () => void tick());

setInterval(() => {
  if (view !== "create" && Date.now() - lastListSync >= STALE_AFTER_MS) {
    stale = true;
    show("sync-notice", "List is not updated", "warning");
    renderDetail();
  }
}, STALE_CHECK_INTERVAL_MS);

let slotsRevision=0;
async function loadSlots() {
  if (!scheduleMode || attempt) return;
  const revision=++slotsRevision;
  const select=$("schedule-slot"); select.replaceChildren(new Option("Choose delivery window", ""));
  if (!draft.pickup || !draft.delivery || !draft.date) return;
  const ref=value=>({type:value.slice(0,value.indexOf(":")),id:value.slice(value.indexOf(":")+1)});
  const p=ref(draft.pickup),d=ref(draft.delivery);
  const query=new URLSearchParams({date:draft.date,pickupType:p.type,pickupId:p.id,deliveryType:d.type,deliveryId:d.id});
  try {
    const data=await api(`/slots?${query}`,{schedule:true});
    if(revision!==slotsRevision)return;
    for(const start of data.slots)select.append(new Option(`${time(start)} – ${time(new Date(Date.parse(start)+1800000))}`,start));
    if(data.slots.includes(draft.slot))select.value=draft.slot;
    else {draft.slot="";persist();}
    if(!data.slots.length)show("form-message","No available windows on this date. Choose another date.");
  } catch(error) { if(revision===slotsRevision)show("form-message",error.message,"error"); }
}
$("schedule-fields").hidden=!scheduleMode;
$("expiry-field").hidden=scheduleMode;
$("expiry").required=!scheduleMode;
$("schedule-date").required=scheduleMode;
$("schedule-slot").required=scheduleMode;
updateActiveTab();
$("preview-status").textContent=scheduleMode ? "Scheduled" : "Open";
$("preview-status").className=`pill status-${scheduleMode ? "scheduled" : "open"}`;
if(scheduleMode)document.title="Schedule order · Friend on Campus";
$("schedule-date").addEventListener("input",()=>{draft.date=$("schedule-date").value;persist();void loadSlots();});
$("schedule-slot").addEventListener("change",()=>{draft.slot=$("schedule-slot").value;persist();});
for(const id of ["pickup","delivery"])$(id).addEventListener("input",()=>void loadSlots());
