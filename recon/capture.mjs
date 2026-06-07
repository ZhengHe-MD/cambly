#!/usr/bin/env node
// Dependency-free Chrome DevTools Protocol network capture.
// Connects to a Chrome instance launched with --remote-debugging-port and
// records every XHR/Fetch/Document request to Cambly hosts (URL, method,
// request headers incl. auth/cookies, request body, response status/headers,
// response body) as JSONL.
//
// Usage: node capture.mjs [--port 9222] [--out capture.jsonl] [--all]
//   --all   capture every request regardless of host/type (noisy)

import { writeFileSync, appendFileSync } from "node:fs";

const args = process.argv.slice(2);
const getArg = (flag, def) => {
  const i = args.indexOf(flag);
  return i >= 0 && args[i + 1] ? args[i + 1] : def;
};
const PORT = getArg("--port", "9222");
const OUT = getArg("--out", new URL("./capture.jsonl", import.meta.url).pathname);
const CAPTURE_ALL = args.includes("--all");

// Reset the output file.
writeFileSync(OUT, "");
const log = (...a) => console.error("[capture]", ...a);

function isInteresting(url, type) {
  if (CAPTURE_ALL) return true;
  try {
    const host = new URL(url).host;
    if (host.includes("cambly")) return true;
  } catch {}
  return type === "XHR" || type === "Fetch";
}

// ---- Minimal CDP client over a single browser-level WebSocket (flatten mode) ----
class CDP {
  constructor(ws) {
    this.ws = ws;
    this.id = 0;
    this.pending = new Map();
    this.handlers = new Map(); // method -> Set<fn(params, sessionId)>
    ws.addEventListener("message", (ev) => this._onMessage(ev.data));
  }
  _onMessage(data) {
    let msg;
    try { msg = JSON.parse(data); } catch { return; }
    if (msg.id !== undefined && this.pending.has(msg.id)) {
      const { resolve, reject } = this.pending.get(msg.id);
      this.pending.delete(msg.id);
      if (msg.error) reject(new Error(msg.error.message));
      else resolve(msg.result);
      return;
    }
    if (msg.method) {
      const set = this.handlers.get(msg.method);
      if (set) for (const fn of set) {
        try { fn(msg.params, msg.sessionId); } catch (e) { log("handler error", e.message); }
      }
    }
  }
  send(method, params = {}, sessionId) {
    const id = ++this.id;
    const payload = { id, method, params };
    if (sessionId) payload.sessionId = sessionId;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.ws.send(JSON.stringify(payload));
    });
  }
  on(method, fn) {
    if (!this.handlers.has(method)) this.handlers.set(method, new Set());
    this.handlers.get(method).add(fn);
  }
}

async function main() {
  // Discover the browser-level WebSocket endpoint.
  const versionRes = await fetch(`http://localhost:${PORT}/json/version`);
  const version = await versionRes.json();
  const browserWsUrl = version.webSocketDebuggerUrl;
  log("connecting to", browserWsUrl);

  const ws = new WebSocket(browserWsUrl);
  await new Promise((res, rej) => {
    ws.addEventListener("open", res, { once: true });
    ws.addEventListener("error", rej, { once: true });
  });
  const cdp = new CDP(ws);
  log("connected. writing to", OUT);

  // requestId -> partial record
  const records = new Map();
  let count = 0;

  const flush = (rec) => {
    appendFileSync(OUT, JSON.stringify(rec) + "\n");
    count++;
    const tag = rec.response?.status ?? "?";
    log(`#${count} ${rec.request?.method ?? "?"} ${tag} ${rec.request?.url ?? ""}`.slice(0, 160));
  };

  const attachToSession = async (sessionId) => {
    try {
      await cdp.send("Network.enable", {}, sessionId);
    } catch (e) { log("Network.enable failed", e.message); }
  };

  // Network event handlers (params carry sessionId via the message envelope).
  cdp.on("Network.requestWillBeSent", (p) => {
    if (!isInteresting(p.request.url, p.type)) return;
    const rec = records.get(p.requestId) || { requestId: p.requestId };
    rec.type = p.type;
    rec.request = {
      url: p.request.url,
      method: p.request.method,
      headers: p.request.headers || {},
      postData: p.request.postData,
      hasPostData: p.request.hasPostData,
    };
    rec.wallTime = p.wallTime;
    records.set(p.requestId, rec);
  });

  cdp.on("Network.requestWillBeSentExtraInfo", (p) => {
    const rec = records.get(p.requestId);
    if (!rec) return;
    rec.request = rec.request || {};
    // Full headers including Cookie / Authorization that the renderer hid.
    rec.request.headersExtra = p.headers || {};
  });

  cdp.on("Network.responseReceived", (p) => {
    const rec = records.get(p.requestId);
    if (!rec) return;
    rec.response = {
      status: p.response.status,
      statusText: p.response.statusText,
      url: p.response.url,
      mimeType: p.response.mimeType,
      headers: p.response.headers || {},
    };
  });

  cdp.on("Network.responseReceivedExtraInfo", (p) => {
    const rec = records.get(p.requestId);
    if (!rec) return;
    rec.response = rec.response || {};
    rec.response.headersExtra = p.headers || {}; // includes set-cookie
  });

  const finalize = async (requestId, sessionId, failed) => {
    const rec = records.get(requestId);
    if (!rec) return;
    records.delete(requestId);
    if (!failed && rec.response) {
      try {
        const body = await cdp.send("Network.getResponseBody", { requestId }, sessionId);
        rec.response.body = body.base64Encoded
          ? Buffer.from(body.body, "base64").toString("utf8")
          : body.body;
      } catch (e) {
        rec.response.bodyError = e.message;
      }
    }
    if (failed) rec.failed = true;
    flush(rec);
  };

  cdp.on("Network.loadingFinished", (p, sessionId) => finalize(p.requestId, sessionId, false));
  cdp.on("Network.loadingFailed", (p, sessionId) => {
    const rec = records.get(p.requestId);
    if (rec) { rec.errorText = p.errorText; finalize(p.requestId, sessionId, true); }
  });

  // Attach to every page target, present and future, in flatten mode.
  cdp.on("Target.attachedToTarget", (p) => {
    if (p.targetInfo?.type === "page" || p.targetInfo?.type === "iframe") {
      log("attached to", p.targetInfo.url?.slice(0, 80));
      attachToSession(p.sessionId);
    }
  });
  cdp.on("Target.targetCreated", async (p) => {
    if (p.targetInfo?.type === "page") {
      try {
        await cdp.send("Target.attachToTarget", { targetId: p.targetInfo.targetId, flatten: true });
      } catch (e) { log("attach failed", e.message); }
    }
  });

  await cdp.send("Target.setAutoAttach", {
    autoAttach: true, waitForDebuggerOnStart: false, flatten: true,
  });
  await cdp.send("Target.setDiscoverTargets", { discover: true });

  log("ready — capturing. Log in and click around in the Chrome window.");
  // Keep alive.
  ws.addEventListener("close", () => { log("connection closed"); process.exit(0); });
}

main().catch((e) => { log("fatal", e); process.exit(1); });
