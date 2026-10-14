/* Godwit native plugin ABI v1 (P0 experiment copy of the design's header). */
#ifndef GODWIT_PLUGIN_H
#define GODWIT_PLUGIN_H
#include <stddef.h>
#include <stdint.h>
#define GODWIT_PLUGIN_ABI 1
typedef struct { void *ptr; size_t len; } godwit_buffer;
typedef int  (*godwit_call_fn)(const char *method, const uint8_t *req, size_t req_len, godwit_buffer *out);
typedef void (*godwit_free_fn)(void *ptr, size_t len);
typedef struct {
    uint32_t       abi_version;
    godwit_call_fn call;
    godwit_free_fn free_buffer;
} godwit_plugin_api;
#endif
