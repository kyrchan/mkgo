/* Freestanding libc bits the compiler may call behind our back. */
#include <stdint.h>
#include <stddef.h>

void *memset(void *d, int c, size_t n) {
    uint8_t *p = d;
    while (n--)
        *p++ = (uint8_t)c;
    return d;
}

void *memcpy(void *d, const void *s, size_t n) {
    uint8_t *pd = d;
    const uint8_t *ps = s;
    while (n--)
        *pd++ = *ps++;
    return d;
}

void *memmove(void *d, const void *s, size_t n) {
    uint8_t *pd = d;
    const uint8_t *ps = s;
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
    const uint8_t *pa = a, *pb = b;
    while (n--) {
        if (*pa != *pb)
            return *pa - *pb;
        pa++;
        pb++;
    }
    return 0;
}
