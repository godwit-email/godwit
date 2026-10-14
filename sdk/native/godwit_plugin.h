/*
 * Godwit native plugin C ABI, version 1.
 *
 * A native plugin is a shared library (plugin.dylib, plugin.so or plugin.dll)
 * next to its plugin.json. Godwit checks the file's SHA-256 against what the
 * user (or the reviewed plugin directory) allowed, then loads it in a child
 * process, `godwit plugin-host`, one process per plugin. The library runs
 * with all of Godwit's rights: this is trusted code, not a sandbox.
 *
 * Requests and responses are JSON. Every response is an envelope:
 *   {"ok": true,  "result": {...}}
 *   {"ok": false, "error": {"code": "...", "message": "...", "retryable": false}}
 * Error codes: invalid_request, unsupported_language, rate_limited, upstream, internal.
 *
 * Methods (v1):
 *   describe   {}                                  -> {"name","version","abi","capabilities"}
 *              Must match plugin.json, or the plugin does not start.
 *   configure  {"config": {...}}                   -> {}
 *              After start and after each settings save; secrets included.
 *   translate  {"texts":[...],"source":"","target":"zh-CN"} -> {"texts":[...],"source":"en"}
 *              source "" asks the plugin to detect it; tags are BCP 47.
 * Unknown methods answer invalid_request.
 */
#ifndef GODWIT_PLUGIN_H
#define GODWIT_PLUGIN_H

#include <stddef.h>
#include <stdint.h>

#define GODWIT_PLUGIN_ABI 1

typedef struct {
    void  *ptr;
    size_t len;
} godwit_buffer;

/*
 * method: NUL-terminated UTF-8. req: the request JSON, owned by the host and
 * valid only during the call (at most 1 MiB). out: a response envelope the
 * plugin allocates (at most 4 MiB); the host copies it and then calls
 * free_buffer on it. Return 0 when out holds an envelope (which may say
 * ok: false); non-zero for a failure below the envelope, out then unused.
 *
 * The host may call concurrently from several threads: call must be thread
 * safe. There is no cancellation; a call the host stops waiting for runs to
 * its end, and a call past the host's deadline (60 s) ends the process.
 * There is no shutdown entry: the host closes the process, so do not rely
 * on destructors. Neither side keeps a pointer it got from the other.
 */
typedef int  (*godwit_call_fn)(const char *method, const uint8_t *req, size_t req_len, godwit_buffer *out);
typedef void (*godwit_free_fn)(void *ptr, size_t len);

typedef struct {
    uint32_t       abi_version; /* set to GODWIT_PLUGIN_ABI */
    godwit_call_fn call;
    godwit_free_fn free_buffer;
} godwit_plugin_api;

/*
 * The one exported symbol. host_abi is the version the host speaks; if it is
 * not GODWIT_PLUGIN_ABI return non-zero and leave api alone. Otherwise fill
 * api and return 0.
 */
#ifndef GODWIT_PLUGIN_NO_INIT_DECL
int godwit_plugin_init(uint32_t host_abi, godwit_plugin_api *api);
#endif

#endif
