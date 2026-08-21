/* Freestanding libc bits the compiler may call behind our back. */
#include <stdint.h>
#include <stddef.h>

extern "C" {

void *memset(void *d, int c, size_t n) {
    uint8_t *p = (uint8_t *)d;
    while (n--)
        *p++ = (uint8_t)c;
    return d;
}

void *memcpy(void *d, const void *s, size_t n) {
    uint8_t *pd = (uint8_t *)d;
    const uint8_t *ps = (const uint8_t *)s;
    while (n--)
        *pd++ = *ps++;
    return d;
}

void *memmove(void *d, const void *s, size_t n) {
    uint8_t *pd = (uint8_t *)d;
    const uint8_t *ps = (const uint8_t *)s;
    if (pd < ps) {
        while (n--)
            *pd++ = *ps++;
    } else {
        pd += n;
        ps += n;
        while (n--)
            *--pd = *--ps;
    }
    return d;
}

int memcmp(const void *a, const void *b, size_t n) {
    const uint8_t *pa = (const uint8_t *)a, *pb = (const uint8_t *)b;
    while (n--) {
        if (*pa != *pb)
            return *pa - *pb;
        pa++;
        pb++;
    }
    return 0;
}

} /* extern "C" */
