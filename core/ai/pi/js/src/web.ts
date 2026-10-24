// The Web APIs pi-ai and the provider SDKs expect, on top of moejs's
// standard library. fetch goes through the Go host (pi.go), which owns the
// HTTP client; everything here only has the shape the SDKs check.
// Imported first by engine.ts: the SDKs read some of these as they load.
import AbortController from "abort-controller/dist/abort-controller.mjs";
import URL from "core-js-pure/actual/url/index.js";
import URLSearchParams from "core-js-pure/actual/url-search-params/index.js";
import { ReadableStream } from "web-streams-polyfill";

declare const hostFetch: (url: string, method: string, headers: string, body: string) => Promise<{ id: number; status: number; headers: string }>;
declare const hostRead: (id: number) => Promise<string | null>; // the next chunk of the body; null at its end
declare const hostAbort: (id: number) => void;
declare const hostRandomBytes: (count: number) => number[];
declare const hostNow: () => number;

class Headers {
  #m = new Map<string, string>();
  constructor(init?: unknown) {
    if (!init) return;
    const entries =
      init instanceof Headers
        ? [...init.entries()]
        : Array.isArray(init)
          ? init
          : Object.entries(init as Record<string, string>);
    for (const [k, v] of entries) this.set(k, v);
  }
  get(k: string) {
    return this.#m.get(k.toLowerCase()) ?? null;
  }
  set(k: string, v: string) {
    this.#m.set(k.toLowerCase(), String(v));
  }
  append(k: string, v: string) {
    const o = this.get(k);
    this.set(k, o === null ? v : `${o}, ${v}`);
  }
  has(k: string) {
    return this.#m.has(k.toLowerCase());
  }
  delete(k: string) {
    this.#m.delete(k.toLowerCase());
  }
  forEach(f: (v: string, k: string) => void) {
    this.#m.forEach(f);
  }
  entries() {
    return this.#m.entries();
  }
  keys() {
    return this.#m.keys();
  }
  values() {
    return this.#m.values();
  }
  [Symbol.iterator]() {
    return this.#m.entries();
  }
}

// Response has getters and a Response name: the OpenAI SDK checks the
// prototype's shape as it loads.
class Response {
  #body: ReadableStream<Uint8Array> | null;
  #status: number;
  #headers: Headers;
  #url: string;
  constructor(body: ReadableStream<Uint8Array> | null = null, status = 200, headers = new Headers(), url = "") {
    this.#body = body;
    this.#status = status;
    this.#headers = headers;
    this.#url = url;
  }
  get body() {
    return this.#body;
  }
  get status() {
    return this.#status;
  }
  get ok() {
    return this.#status >= 200 && this.#status < 300;
  }
  get statusText() {
    return "";
  }
  get headers() {
    return this.#headers;
  }
  get url() {
    return this.#url;
  }
  get [Symbol.toStringTag]() {
    return "Response";
  }
  async text() {
    if (!this.#body) return "";
    const reader = this.#body.getReader();
    const dec = new TextDecoder();
    let out = "";
    for (;;) {
      const { done, value } = await reader.read();
      if (done) return out + dec.decode();
      out += dec.decode(value, { stream: true });
    }
  }
  async json() {
    return JSON.parse(await this.text());
  }
  async arrayBuffer() {
    return new TextEncoder().encode(await this.text()).buffer;
  }
}

async function fetch(input: unknown, init: { method?: string; headers?: unknown; body?: unknown; signal?: AbortSignal } = {}) {
  const url = typeof input === "string" ? input : String((input as { url?: string }).url ?? input);
  const headers = Object.fromEntries(new Headers(init.headers).entries());
  // The SDKs send JSON text; a byte body (a compressed one) is not supported yet.
  if (init.body != null && typeof init.body !== "string") throw new TypeError("fetch: only a string body is supported");
  init.signal?.throwIfAborted();
  const r = await hostFetch(url, init.method ?? "GET", JSON.stringify(headers), init.body ?? "");
  init.signal?.addEventListener("abort", () => hostAbort(r.id));
  const enc = new TextEncoder();
  const body = new ReadableStream<Uint8Array>({
    async pull(c) {
      const s = await hostRead(r.id);
      if (s === null) c.close();
      else c.enqueue(enc.encode(s));
    },
    cancel() {
      hostAbort(r.id);
    },
  });
  return new Response(body, r.status, new Headers(JSON.parse(r.headers)), url);
}

// Unused by the JSON requests made here; the SDKs only test for them.
class Blob {}
class File extends Blob {}
class FormData {}
for (const [c, name] of [
  [Response, "Response"],
  [Blob, "Blob"],
  [File, "File"],
  [FormData, "FormData"],
] as const) {
  Object.defineProperty(c, "name", { value: name });
}

const g = globalThis as Record<string, unknown>;
g.AbortController ??= AbortController;
const signal = Object.getPrototypeOf(new AbortController().signal);
signal.throwIfAborted ??= function (this: AbortSignal) {
  if (this.aborted) throw this.reason ?? new Error("aborted");
};
g.URL ??= URL;
g.URLSearchParams ??= URLSearchParams;
g.ReadableStream ??= ReadableStream;
g.Headers ??= Headers;
g.Response ??= Response;
g.Blob ??= Blob;
g.File ??= File;
g.FormData ??= FormData;
g.fetch ??= fetch;
g.performance ??= { now: hostNow };
g.crypto ??= {
  getRandomValues(a: Uint8Array) {
    a.set(hostRandomBytes(a.byteLength));
    return a;
  },
};
// The SDKs log nothing Godwit needs; their warnings are dropped.
const quiet = () => {};
g.console ??= { log: quiet, info: quiet, warn: quiet, error: quiet, debug: quiet };
