// Local development only: Next.js and the existing gateway share one browser origin.
import http from "node:http";
import https from "node:https";
import next from "next";

const port = Number(process.env.FOC_DEV_PORT ?? "3001");
if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error("FOC_DEV_PORT must be a valid port.");
const gateway = new URL(process.env.FOC_DEV_GATEWAY_URL ?? "http://localhost:3000");
if (!["http:", "https:"].includes(gateway.protocol) || !["localhost", "127.0.0.1", "[::1]"].includes(gateway.hostname)
  || gateway.username || gateway.password || gateway.pathname !== "/" || gateway.search || gateway.hash) {
  throw new Error("FOC_DEV_GATEWAY_URL must be a loopback HTTP(S) origin.");
}
if (gateway.port === String(port)) throw new Error("The frontend and gateway must use different ports.");
process.env.USER_SERVICE_URL ??= gateway.origin;
const app = next({ dev: true, hostname: "localhost", port });
await app.prepare();
const handle = app.getRequestHandler();
const upgrade = app.getUpgradeHandler();
const origins = new Set([`http://localhost:${port}`, `http://127.0.0.1:${port}`]);
const hosts = new Set([`localhost:${port}`, `127.0.0.1:${port}`]);

const server = http.createServer((request, response) => {
  if (!hosts.has(request.headers.host) || (request.headers.origin && !origins.has(request.headers.origin))) {
    response.writeHead(403).end("Local development origin required.");
    return;
  }
  const pathname = new URL(request.url, "http://localhost").pathname;
  if (!(pathname.startsWith("/api/") || pathname === "/orders" || pathname.startsWith("/orders/"))) {
    void handle(request, response).catch(() => {
      if (!response.headersSent) response.writeHead(500);
      response.end("Frontend request failed.");
    });
    return;
  }
  if (!["GET", "HEAD", "OPTIONS"].includes(request.method) && !request.headers.origin) {
    response.writeHead(403).end("Local development origin required.");
    return;
  }
  const headers = { ...request.headers, host: gateway.host };
  // Validate the incoming origin above before translating it for the local services.
  if (headers.origin) headers.origin = gateway.origin;
  const transport = gateway.protocol === "https:" ? https : http;
  const upstream = transport.request(gateway, { method: request.method, path: request.url, headers }, (result) => {
    response.writeHead(result.statusCode ?? 502, result.headers);
    result.pipe(response);
    result.on("error", () => response.destroy());
  });
  upstream.setTimeout(30_000, () => upstream.destroy());
  upstream.on("error", () => {
    if (!response.headersSent) response.writeHead(502, { "Content-Type": "application/json" });
    response.end(JSON.stringify({ error: { code: "gateway_unavailable", message: "The local gateway is unavailable." } }));
  });
  response.on("close", () => upstream.destroy());
  request.pipe(upstream);
});
server.on("upgrade", (request, socket, head) => {
  if (!hosts.has(request.headers.host) || (request.headers.origin && !origins.has(request.headers.origin))) {
    socket.destroy();
    return;
  }
  void upgrade(request, socket, head);
});
server.listen(port, "127.0.0.1", () => {
  console.log(`Frontend: http://localhost:${port} · Gateway: ${gateway.origin}`);
});
