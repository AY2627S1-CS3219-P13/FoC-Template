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

export function creationBody(draft) {
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
    expiresAt: expiryISO(draft.expiry),
  });
}
