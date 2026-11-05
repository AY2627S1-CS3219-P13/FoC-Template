// Service worker for nearby errand alerts (scope /orders/). It only shows
// notifications; it has no fetch handler and never caches pages or data.
"use strict";

const FALLBACK = {
  title: "New errand nearby",
  body: "Open Help a friend to see nearby errands.",
  url: "/orders?view=open",
};

// Turn a decrypted push payload into notification options. Anything unexpected
// still shows a notification (browsers require one per push) with safe text.
function notificationFor(data) {
  const valid = data !== null && typeof data === "object" && data.v === 1 && typeof data.orderId === "string";
  const text = (value, fallback) => (valid && typeof value === "string" && value ? value.slice(0, 200) : fallback);
  const url = valid && typeof data.url === "string" && data.url.startsWith("/orders?") ? data.url : FALLBACK.url;
  return {
    title: text(data?.title, FALLBACK.title),
    options: {
      body: text(data?.body, FALLBACK.body),
      tag: valid ? `errand-${data.orderId}` : "errand",
      data: { url, orderId: valid ? data.orderId : null },
    },
  };
}

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (event) => event.waitUntil(self.clients.claim()));

self.addEventListener("push", (event) => {
  let data = null;
  try {
    data = event.data ? event.data.json() : null;
  } catch {
    data = null;
  }
  const { title, options } = notificationFor(data);
  event.waitUntil(self.registration.showNotification(title, options));
});

// Reuse an open Orders tab when there is one; otherwise open the errand.
self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const { url, orderId } = event.notification.data || {};
  const target = new URL(url || FALLBACK.url, self.location.origin).href;
  event.waitUntil(
    (async () => {
      const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
      const open = windows.find((client) => new URL(client.url).pathname.startsWith("/orders"));
      if (open) {
        await open.focus();
        open.postMessage({ type: "open-errand", orderId: orderId || null });
        return;
      }
      await self.clients.openWindow(target);
    })(),
  );
});

self.notificationFor = notificationFor;
