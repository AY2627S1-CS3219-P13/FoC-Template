// Preserve Credit's signed-64-bit integer contract; browser Number cannot.
export function creationKey() {
  // getRandomValues also works on local HTTP test/development origins where
  // randomUUID is unavailable. Never fall back to timestamps or Math.random.
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) =>
    byte.toString(16).padStart(2, "0"),
  ).join("");
}

export function positiveInteger(value) {
  if (!/^[0-9]+$/.test(String(value)))
    throw new Error("Use positive whole numbers for quantities and credits.");
  const n = BigInt(value);
  if (n < 1n || n > 9223372036854775807n)
    throw new Error("The supported integer range is 1–9223372036854775807.");
  return n;
}

export function encodeJSON(value) {
  if (typeof value === "bigint") return value.toString();
  if (Array.isArray(value)) return `[${value.map(encodeJSON).join(",")}]`;
  if (value !== null && typeof value === "object")
    return `{${Object.entries(value)
      .map(([k, v]) => `${JSON.stringify(k)}:${encodeJSON(v)}`)
      .join(",")}}`;
  return JSON.stringify(value);
}

export function decodeJSON(text) {
  // Tokenize strings too, so digits inside names/IDs are never rewritten.
  return JSON.parse(
    text.replace(
      /"(?:\\.|[^"\\])*"|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/g,
      (token, number) => {
        if (
          number &&
          /^-?\d+$/.test(number) &&
          !Number.isSafeInteger(Number(number))
        )
          return JSON.stringify(number);
        return token;
      },
    ),
  );
}

export function expiryISO(value) {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(value))
    throw new Error("Choose an expiry in Singapore time.");
  const date = new Date(`${value}:00+08:00`);
  if (!Number.isFinite(date.getTime()))
    throw new Error("Choose a valid expiry.");
  return date.toISOString();
}

export function newer(previous, incoming) {
  if (
    previous?.id === incoming.id &&
    BigInt(previous.version) > BigInt(incoming.version)
  )
    return previous;
  return incoming;
}

export function creationBody(draft, windowStart) {
  const ref = (value) => {
    const split = value.indexOf(":");
    if (split < 1) throw new Error("Choose both pickup and delivery.");
    return { type: value.slice(0, split), id: value.slice(split + 1) };
  };
  if (!draft.items.length) throw new Error("Add at least one item.");
  if (draft.pickup === draft.delivery)
    throw new Error("Pickup and delivery must be different places.");
  return encodeJSON({
    pickup: ref(draft.pickup),
    delivery: ref(draft.delivery),
    items: draft.items.map((item) => ({
      ...item,
      quantity: positiveInteger(item.quantity),
    })),
    offeredCredits: positiveInteger(draft.offer),
    ...(windowStart ? {windowStart} : {expiresAt: expiryISO(draft.expiry)}),
  });
}
// Match PostgreSQL's creation DESC, ID ASC history ordering (IDs are ASCII).
export function newestFirst(a, b) {
  return Date.parse(b.createdAt) - Date.parse(a.createdAt) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0);
}

// Same mean Earth radius and rounding as the server's proximity.go, so merged
// scheduled requests rank exactly like regular ones.
const EARTH_RADIUS_METERS = 6371000;

export function distanceMeters(a, b) {
  const radians = (degrees) => (degrees * Math.PI) / 180;
  const dLat = radians(b.latitude - a.latitude);
  const dLng = radians(b.longitude - a.longitude);
  const h =
    Math.sin(dLat / 2) ** 2 +
    Math.cos(radians(a.latitude)) * Math.cos(radians(b.latitude)) * Math.sin(dLng / 2) ** 2;
  return Math.round(2 * EARTH_RADIUS_METERS * Math.asin(Math.min(1, Math.sqrt(h))));
}

// A supplier pickup is located at its registered campus location.
export function pickupLocationId(order) {
  return order.pickup.type === "location" ? order.pickup.id : order.pickup.locationId;
}

// null means "same place": closer than the coordinates can meaningfully tell.
export function formatDistance(meters) {
  if (meters < 50) return null;
  return meters < 1000 ? `${Math.round(meters / 10) * 10} m` : `${(meters / 1000).toFixed(1)} km`;
}

// Mirror the server's open-list order. distanceOf returns metres or null;
// unknown distances follow known ones. Ties: expiry, creation, then ID.
export function openComparator(sort, distanceOf) {
  return (a, b) => {
    if (sort === "distance") {
      const da = distanceOf(a), db = distanceOf(b);
      if ((da === null) !== (db === null)) return da === null ? 1 : -1;
      if (da !== null && da !== db) return da - db;
    }
    if (sort === "credits" && BigInt(a.offeredCredits) !== BigInt(b.offeredCredits))
      return BigInt(a.offeredCredits) > BigInt(b.offeredCredits) ? -1 : 1;
    return (
      Date.parse(a.expiresAt) - Date.parse(b.expiresAt) ||
      Date.parse(a.createdAt) - Date.parse(b.createdAt) ||
      (a.id < b.id ? -1 : a.id > b.id ? 1 : 0)
    );
  };
}

export function scheduledHistoryPage(orders, page, pageStart, nextPageStart) {
  return orders.filter(order =>
    (page === 1 || (pageStart && newestFirst(order, pageStart) >= 0)) &&
    (!nextPageStart || newestFirst(order, nextPageStart) < 0),
  );
}

// Nearby alerts: the panel state from browser capability, permission and the
// server's view of this account. `subscribed` means this browser has a subscription.
export function alertState({ supported, secure, permission, pushAvailable, preference, subscribed, devices }) {
  if (!supported) return { key: "unsupported", message: "This browser can't receive alerts. Use Chrome, Edge, Firefox, or Safari on macOS." };
  if (!secure) return { key: "insecure", message: "Alerts need a secure address (HTTPS or localhost)." };
  if (!pushAvailable) return { key: "unavailable", message: "Nearby alerts are not available on this server right now." };
  if (permission === "denied")
    return { key: "blocked", message: "Notifications are blocked for this site. Allow them in your browser's site settings, then reload this page." };
  if (preference?.enabled && subscribed) return { key: "on", message: `On for this device · ${radiusLabel(preference.radiusMeters)}` };
  if (preference?.enabled && devices > 0)
    return { key: "elsewhere", message: "On for another of your devices. Turn alerts on here to also get them on this device." };
  return { key: "off", message: "Off. Get a notification when a new errand appears near you, even with this page closed." };
}

export const radiusLabel = (meters) => (meters >= 1000 ? `within ${meters / 1000} km` : `within ${meters} m`);

// PushManager.subscribe needs the VAPID public key as bytes.
export function base64UrlBytes(value) {
  const base64 = value.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(value.length / 4) * 4, "=");
  return Uint8Array.from(atob(base64), (c) => c.charCodeAt(0));
}

// Send only the documented fields; toJSON() also carries expirationTime.
export function subscriptionBody(json) {
  return { endpoint: json.endpoint, keys: { p256dh: json.keys?.p256dh, auth: json.keys?.auth } };
}

// /orders?view=open&order=<id>, the link a notification opens.
export function deepLink(search) {
  const query = new URLSearchParams(search);
  const order = query.get("order");
  return query.get("view") === "open" ? { view: "open", order: order && order.length <= 160 ? order : null } : null;
}
