// A minimal ABI v1 plugin for the plugin-host tests: describe, nothing else.
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include "godwit_plugin.h"

__attribute__((constructor)) static void loaded(void) {
    const char *p = getenv("ECHO_MARKER");
    if (p) {
        FILE *f = fopen(p, "w");
        if (f) { fputs("loaded", f); fclose(f); }
    }
}

static int reply(const char *s, godwit_buffer *out) {
    size_t n = strlen(s);
    out->ptr = malloc(n);
    if (!out->ptr) return -1;
    memcpy(out->ptr, s, n);
    out->len = n;
    return 0;
}

static int call(const char *method, const uint8_t *req, size_t len, godwit_buffer *out) {
    (void)req; (void)len;
    if (strcmp(method, "noisy") == 0) { /* libraries print: it must not reach the protocol */
        printf("noise");
        fflush(stdout);
        return reply("{\"ok\":true,\"result\":{}}", out);
    }
    if (strcmp(method, "describe") == 0)
        return reply("{\"ok\":true,\"result\":{\"name\":\"echo\",\"version\":\"1\",\"abi\":1,\"capabilities\":[\"translate\"]}}", out);
    return reply("{\"ok\":false,\"error\":{\"code\":\"invalid_request\",\"message\":\"unknown method\"}}", out);
}

static void release(void *p, size_t n) { (void)n; free(p); }

int godwit_plugin_init(uint32_t host_abi, godwit_plugin_api *api) {
    if (host_abi != GODWIT_PLUGIN_ABI || !api) return -1;
    api->abi_version = GODWIT_PLUGIN_ABI;
    api->call = call;
    api->free_buffer = release;
    return 0;
}
