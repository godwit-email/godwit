#include <stdio.h>
#include <stdlib.h>
__attribute__((constructor)) static void initialize(void) {
    const char *p = getenv("P0_MARKER");
    if (p) { FILE *f = fopen(p, "w"); if (f) { fputs("EXECUTED_AT_DLOPEN", f); fclose(f); } }
}
