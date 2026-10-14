#include "_cgo_export.h"
void fill_api(godwit_plugin_api *api) {
    api->abi_version = GODWIT_PLUGIN_ABI;
    api->call = (godwit_call_fn)godwit_plugin_call;
    api->free_buffer = (godwit_free_fn)godwit_plugin_free;
}
