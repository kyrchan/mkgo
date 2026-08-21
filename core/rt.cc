/* Freestanding C runtime surface required by third_party/wasm3 and the
 * WASI glue: heap over the mm pool, abort, vsnprintf subset, str helpers.
 * All allocations carry a 16-byte header so free/realloc know the size. */
#include "rt.h"
#include "mm.h"
#include "plat.h"

extern "C" {

/* ---- heap ---- */

struct hdr {
    uint64_t size; /* total block size incl. header, 16-aligned */
    struct hdr *next_free;
    uint64_t magic;
    uint64_t pad;
};

#define RT_MAGIC 0x5254484452414D31ULL /* "RTHDRam1" */

static constexpr int NCLASS = 20; /* classes 4..23 => 16B .. 8MB */
static hdr *bins[NCLASS];

/* smallest class c with 2^c >= n, or NCLASS when out of bands */
static int cls_of(uint64_t n) {
    int c = 4;
    while (c < NCLASS && ((uint64_t)1 << c) < n)
        c++;
    return c;
}

void *rt_malloc(uint64_t n) {
    if (!n)
        n = 1;
    uint64_t need = ((n + sizeof(hdr) + 15) & ~15ULL);
    int c = cls_of(need);
    /* Every binned block is rounded to its FULL class size so any pop
     * always satisfies the request -- no capacity surprises. */
    hdr *h;
    if (c < NCLASS) {
        uint64_t clsz = (uint64_t)1 << c;
        if (bins[c]) {
            h = bins[c];
            bins[c] = h->next_free; /* size/magic stay intact */
        } else {
            h = (hdr *)mm_alloc(clsz, 16);
            if (!h)
                return 0;
            h->size = clsz;
        }
    } else {
        /* oversized: exact allocation, never binned (leaked on free) */
        h = (hdr *)mm_alloc(need, 16);
        if (!h)
            return 0;
        h->size = need;
    }
    h->magic = RT_MAGIC;
    return (void *)(h + 1);
}

void rt_free(void *p) {
    if (!p)
        return;
    hdr *h = (hdr *)p - 1;
    if (h->magic != RT_MAGIC)
        return; /* never free foreign blocks (raw mm_alloc users) */
    int c = cls_of(h->size);
    if (c >= NCLASS)
        return; /* oversized: leak (rare in wasm3 patterns) */
    h->next_free = bins[c];
    bins[c] = h;
}

void *rt_realloc(void *p, uint64_t n) {
    if (!p)
        return rt_malloc(n);
    hdr *h = (hdr *)p - 1;
    uint64_t cap = h->size - sizeof(hdr);
    if (!h->magic)
        return p; /* foreign/raw block: cannot grow safely, leak-stable */
    if (n <= cap)
        return p; /* fits in existing capacity */
    void *q = rt_malloc(n);
    if (!q)
        return 0;
    uint64_t cp = cap < n ? cap : n;
    uint8_t *dq = (uint8_t *)q, *sp = (uint8_t *)p;
    for (uint64_t i = 0; i < cp; i++)
        dq[i] = sp[i];
    rt_free(p);
    return q;
}

void *rt_calloc(uint64_t n, uint64_t sz) {
    uint64_t total = n * sz;
    uint8_t *p = (uint8_t *)rt_malloc(total);
    if (!p)
        return 0;
    for (uint64_t i = 0; i < total; i++)
        p[i] = 0;
    return p;
}

/* ---- libc bits wasm3 calls ---- */

void abort(void) {
    console_puts("[rt] ABORT\n");
    cpu_halt();
}

void exit(int rc) {
    console_puts("[rt] exit ");
    console_hex64((uint64_t)rc);
    console_puts("\n");
    cpu_halt();
}

static char *put(char *p, char *e, char c) {
    if (p < e)
        *p = c;
    return p + 1;
}

static char *puts_(char *p, char *e, const char *s) {
    while (*s)
        p = put(p, e, *s++);
    return p;
}

static char *putu(char *p, char *e, uint64_t v, int base, int sgn, int width,
                  char pad) {
    char tmp[24];
    int i = 0;
    if (sgn && (int64_t)v < 0) {
        p = put(p, e, '-');
        v = (uint64_t)(-(int64_t)v);
    }
    do {
        int d = (int)(v % (uint64_t)base);
        tmp[i++] = (char)(d < 10 ? '0' + d : 'a' + d - 10);
        v /= (uint64_t)base;
    } while (v);
    while (i < width--)
        p = put(p, e, pad);
    while (i--)
        p = put(p, e, tmp[i]);
    return p;
}

/* %s %d %i %u %x %c %p %% -- enough for wasm3 error strings */
int vsnprintf(char *out, uint64_t n, const char *fmt, __builtin_va_list ap) {
    char *p = out, *e = out + (n ? n - 1 : 0);
    for (; *fmt; fmt++) {
        if (*fmt != '%') {
            p = put(p, e, *fmt);
            continue;
        }
        fmt++;
        int width = 0;
        char pad = ' ';
        if (*fmt == '0') {
            pad = '0';
            fmt++;
        }
        while (*fmt >= '0' && *fmt <= '9')
            width = width * 10 + (*fmt++ - '0');
        int lng = 0;
        while (*fmt == 'l')
            lng++, fmt++;
        switch (*fmt) {
        case 's': {
            const char *s = __builtin_va_arg(ap, const char *);
            p = puts_(p, e, s ? s : "(null)");
            break;
        }
        case 'd':
        case 'i':
            p = putu(p, e, __builtin_va_arg(ap, long), 10, 1, width, pad);
            break;
        case 'u':
            p = putu(p, e, __builtin_va_arg(ap, unsigned long), 10, 0, width, pad);
            break;
        case 'x':
            p = putu(p, e, __builtin_va_arg(ap, unsigned long), 16, 0, width, pad);
            break;
        case 'c':
            p = put(p, e, (char)__builtin_va_arg(ap, int));
            break;
        case 'p':
            p = puts_(p, e, "0x");
            p = putu(p, e, (uint64_t)__builtin_va_arg(ap, void *), 16, 0, 16, '0');
            break;
        case '%':
            p = put(p, e, '%');
            break;
        default:
            p = put(p, e, '%');
            p = put(p, e, *fmt);
        }
    }
    *p = 0;
    return (int)(p - out);
}

int vsprintf(char *out, const char *fmt, __builtin_va_list ap) {
    return vsnprintf(out, (uint64_t)-1, fmt, ap);
}

int snprintf(char *out, uint64_t n, const char *fmt, ...) {
    __builtin_va_list ap;
    __builtin_va_start(ap, fmt);
    int r = vsnprintf(out, n, fmt, ap);
    __builtin_va_end(ap);
    return r;
}

uint64_t strlen(const char *s) {
    uint64_t n = 0;
    while (s[n])
        n++;
    return n;
}

int strcmp(const char *a, const char *b) {
    while (*a && *a == *b)
        a++, b++;
    return (int)(uint8_t)*a - (int)(uint8_t)*b;
}

int strncmp(const char *a, const char *b, uint64_t n) {
    while (n--) {
        if (*a != *b)
            return (int)(uint8_t)*a - (int)(uint8_t)*b;
        if (!*a)
            break;
        a++, b++;
    }
    return 0;
}

char *strchr(const char *s, int c) {
    for (;; s++) {
        if (*s == (char)c)
            return (char *)s;
        if (!*s)
            return 0;
    }
}

/* ---- libc names wasm3 links against (thin aliases over the rt heap) ---- */

void *malloc(uint64_t n) { return rt_malloc(n); }
void *calloc(uint64_t n, uint64_t sz) { return rt_calloc(n, sz); }
void *realloc(void *p, uint64_t n) { return rt_realloc(p, n); }
void free(void *p) { rt_free(p); }

static uint64_t strtox(const char *s, char **end, int base) {
    uint64_t v = 0;
    while (*s == ' ' || *s == '\t')
        s++;
    int neg = 0;
    if (*s == '-') {
        neg = 1;
        s++;
    } else if (*s == '+') {
        s++;
    }
    if ((base == 16 || base == 0) && s[0] == '0' && (s[1] == 'x' || s[1] == 'X')) {
        base = 16;
        s += 2;
    } else if (base == 0) {
        base = *s == '0' ? 8 : 10;
    }
    while (*s) {
        int d;
        if (*s >= '0' && *s <= '9')
            d = *s - '0';
        else if (*s >= 'a' && *s <= 'f')
            d = *s - 'a' + 10;
        else if (*s >= 'A' && *s <= 'F')
            d = *s - 'A' + 10;
        else
            break;
        if (d >= base)
            break;
        v = v * (uint64_t)base + (uint64_t)d;
        s++;
    }
    if (end)
        *(char **)end = (char *)s;
    return neg ? (uint64_t)(-(int64_t)v) : v;
}

uint64_t strtoul(const char *s, char **end, int base) {
    return strtox(s, end, base);
}

uint64_t strtoull(const char *s, char **end, int base) {
    return strtox(s, end, base);
}

double strtod(const char *s, char **end) {
    /* decimal-only subset: [-]digits[.frac][e[+-]exp] */
    while (*s == ' ')
        s++;
    double sign = 1;
    if (*s == '-') {
        sign = -1;
        s++;
    }
    double v = 0;
    while (*s >= '0' && *s <= '9')
        v = v * 10 + (*s++ - '0');
    if (*s == '.') {
        s++;
        double frac = 0, scale = 0.1;
        while (*s >= '0' && *s <= '9') {
            frac += (*s++ - '0') * scale;
            scale *= 0.1;
        }
        v += frac;
    }
    if (*s == 'e' || *s == 'E') {
        s++;
        int es = 1;
        if (*s == '+')
            s++;
        else if (*s == '-') {
            es = -1;
            s++;
        }
        int e = 0;
        while (*s >= '0' && *s <= '9')
            e = e * 10 + (*s++ - '0');
        double p = 1;
        for (int i = 0; i < e; i++)
            p *= es > 0 ? 10 : 0.1;
        v *= p;
    }
    if (end)
        *(char **)end = (char *)s;
    return sign * v;
}

} /* extern "C" */
