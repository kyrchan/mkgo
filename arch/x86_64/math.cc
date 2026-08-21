/* SSE2 shims for the float helpers wasm3's executor calls. */
#include <stdint.h>

extern "C" {

double sqrt(double x) {
    double r;
    __asm__("sqrtsd %1, %0" : "=x"(r) : "x"(x));
    return r;
}

float sqrtf(float x) {
    float r;
    __asm__("sqrtss %1, %0" : "=x"(r) : "x"(x));
    return r;
}

double fabs(double x) {
    uint64_t xi = *(uint64_t *)&x;
    xi &= 0x7FFFFFFFFFFFFFFFULL;
    return *(double *)&xi;
}

float fabsf(float x) {
    uint32_t xi = *(uint32_t *)&x;
    xi &= 0x7FFFFFFF;
    return *(float *)&xi;
}

double floor(double x) {
    double r;
    __asm__("roundsd $1, %1, %0" : "=x"(r) : "x"(x));
    return r;
}

float floorf(float x) {
    float r;
    __asm__("roundss $1, %1, %0" : "=x"(r) : "x"(x));
    return r;
}

double ceil(double x) {
    double r;
    __asm__("roundsd $2, %1, %0" : "=x"(r) : "x"(x));
    return r;
}

float ceilf(float x) {
    float r;
    __asm__("roundss $2, %1, %0" : "=x"(r) : "x"(x));
    return r;
}

double trunc(double x) {
    double r;
    __asm__("roundsd $3, %1, %0" : "=x"(r) : "x"(x));
    return r;
}

float truncf(float x) {
    float r;
    __asm__("roundss $3, %1, %0" : "=x"(r) : "x"(x));
    return r;
}

double copysign(double x, double y) {
    uint64_t xi = *(uint64_t *)&x, yi = *(uint64_t *)&y;
    xi = (xi & 0x7FFFFFFFFFFFFFFFULL) | (yi & 0x8000000000000000ULL);
    return *(double *)&xi;
}

float copysignf(float x, float y) {
    uint32_t xi = *(uint32_t *)&x, yi = *(uint32_t *)&y;
    xi = (xi & 0x7FFFFFFF) | (yi & 0x80000000);
    return *(float *)&xi;
}

} /* extern "C" */
