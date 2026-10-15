/* SSE2 implementations of the float helpers the engine's f32/f64 opcodes
 * lower to. The comment here used to say "wasm3's executor calls", which was
 * wrong and cost this file a deletion during the cutover: the OWNED engine
 * needs them just as much, because __builtin_ceil/floor/trunc/sqrt on an f32
 * operand lower to ceilf/floorf/truncf/sqrtf libcalls. It is an arch shim
 * because the implementations are SSE2 -- core/ stays free of arch code
 * (AGENTS rule 1) and reaches these through the ordinary libm names. */
#include <stdint.h>
#include <string.h>

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
    uint64_t xi;
    memcpy(&xi, &x, 8);
    xi &= 0x7FFFFFFFFFFFFFFFULL;
    double r;
    memcpy(&r, &xi, 8);
    return r;
}

float fabsf(float x) {
    uint32_t xi;
    memcpy(&xi, &x, 4);
    xi &= 0x7FFFFFFF;
    float r;
    memcpy(&r, &xi, 4);
    return r;
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
    uint64_t xi, yi;
    memcpy(&xi, &x, 8);
    memcpy(&yi, &y, 8);
    xi = (xi & 0x7FFFFFFFFFFFFFFFULL) | (yi & 0x8000000000000000ULL);
    double r;
    memcpy(&r, &xi, 8);
    return r;
}

float copysignf(float x, float y) {
    uint32_t xi, yi;
    memcpy(&xi, &x, 4);
    memcpy(&yi, &y, 4);
    xi = (xi & 0x7FFFFFFF) | (yi & 0x80000000);
    float r;
    memcpy(&r, &xi, 4);
    return r;
}

} /* extern "C" */
