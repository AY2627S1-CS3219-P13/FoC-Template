// One controller per editor. Sequence fencing complements AbortController: an
// aborted HTTP request may still finish on the server or in a test transport.
export function quoteUUID() {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 15) | 64;
  bytes[8] = (bytes[8] & 63) | 128;
  const hex = Array.from(bytes, b => b.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0,8)}-${hex.slice(8,12)}-${hex.slice(12,16)}-${hex.slice(16,20)}-${hex.slice(20)}`;
}

export function createSuggestions({request, render, debounceMs = 400, refreshMs = 30000}) {
  let id = quoteUUID(), revision = 0, fingerprint, inputs, quote = null;
  let timer, expiryTimer, controller, generation = 0;
  const stop = () => {
    generation++;
    clearTimeout(timer);
    clearTimeout(expiryTimer);
    controller?.abort();
    controller = null;
    quote = null;
  };
  async function calculate() {
    stop();
    if (!inputs) return;
    const current = generation;
    const draft = {id, revision};
    const abort = new AbortController();
    controller = abort;
    const timeout = setTimeout(() => abort.abort(), 2500);
    render({state: "loading"});
    try {
      const result = await request({...inputs, draft}, quoteUUID(), abort.signal);
      if (current !== generation) return;
      if (result.draft?.id !== draft.id || result.draft?.revision !== draft.revision ||
          !Number.isSafeInteger(result.recommendedCredits) || result.recommendedCredits < 1 ||
          !Number.isFinite(Date.parse(result.validUntil)) || Date.parse(result.validUntil) <= Date.now())
        throw new Error("Invalid or expired suggestion");
      quote = result;
      render({state: "ready", quote});
      expiryTimer = setTimeout(() => {
        if (current === generation) void calculate();
      }, Math.max(1, Math.min(refreshMs, Date.parse(result.validUntil) - Date.now())));
    } catch (error) {
      if (current === generation) {
        render({state: "unavailable", sessionExpired: error.status === 401});
        if (current === generation && error.status !== 401) {
          expiryTimer = setTimeout(() => {
            if (current === generation) void calculate();
          }, Math.max(refreshMs, error.retryAfterMs || 0));
        }
      }
    } finally {
      clearTimeout(timeout);
    }
  }
  return {
    update(next) {
      const nextFingerprint = JSON.stringify(next);
      if (nextFingerprint === fingerprint) return;
      fingerprint = nextFingerprint;
      inputs = next;
      revision++;
      stop();
      render({state: next ? "loading" : "incomplete"});
      if (next) timer = setTimeout(() => void calculate(), debounceMs);
    },
    current() { return quote && Date.parse(quote.validUntil) > Date.now() ? quote : null; },
    reset() {
      stop(); inputs = null; fingerprint = undefined; id = quoteUUID(); revision = 0;
      render({state: "incomplete"});
    },
  };
}
