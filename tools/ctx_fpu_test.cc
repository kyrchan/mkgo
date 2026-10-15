/* FPU/SSE state must survive a context switch.
 *
 * ctx_switch used to preserve only the callee-saved GPRs. That is legal
 * for an ordinary call (SysV makes xmm0-15 caller-saved) but not for a
 * session switch: the scheduler switches out in the middle of the owned
 * interpreter, and wasm guests use floats (graphics.wasm alpha blends).
 * A resumed session then computed with the previous session's registers.
 *
 * This test drives the real arch/x86_64/ctx.S through two contexts on two
 * stacks, exactly as the scheduler does: context A loads a distinct
 * 16-byte pattern into all 16 xmm registers plus a distinctive MXCSR,
 * switches to B, B clobbers every register and switches back, and A
 * verifies. Against the pre-fix ctx.S every register mismatches.
 */
#include "core/ctx.h"

#include <cstdint>
#include <cstdio>
#include <cstring>

namespace {

constexpr size_t kStackBytes = 256 * 1024;

/* Distinct pattern per register; B overwrites them with this. */
alignas(16) uint8_t g_pattern[16][16];
constexpr uint8_t kClobberByte = 0xAB;
constexpr uint32_t kTestMxcsr = 0x0000E040u; /* DAZ|FTZ|round-to-zero|masked */
constexpr uint32_t kClobberMxcsr = 0x00001F80u;

void set_mxcsr(uint32_t value) {
    __asm__ volatile("ldmxcsr %0" : : "m"(value));
}

uint32_t get_mxcsr() {
    uint32_t value = 0;
    __asm__ volatile("stmxcsr %0" : "=m"(value));
    return value;
}

alignas(16) uint8_t g_stack_a[kStackBytes];
alignas(16) uint8_t g_stack_b[kStackBytes];

uint64_t *g_main_sp;
uint64_t *g_a_sp;
uint64_t *g_b_sp;
uint64_t *g_a_saved;
uint64_t *g_b_saved;
int g_which;
int g_failures;
uint32_t g_seen_mxcsr;

void load_patterns() {
    for (int i = 0; i < 16; ++i) {
        switch (i) {
        case 0: __asm__ volatile("movdqu %0, %%xmm0" ::"m"(g_pattern[0])); break;
        case 1: __asm__ volatile("movdqu %0, %%xmm1" ::"m"(g_pattern[1])); break;
        case 2: __asm__ volatile("movdqu %0, %%xmm2" ::"m"(g_pattern[2])); break;
        case 3: __asm__ volatile("movdqu %0, %%xmm3" ::"m"(g_pattern[3])); break;
        case 4: __asm__ volatile("movdqu %0, %%xmm4" ::"m"(g_pattern[4])); break;
        case 5: __asm__ volatile("movdqu %0, %%xmm5" ::"m"(g_pattern[5])); break;
        case 6: __asm__ volatile("movdqu %0, %%xmm6" ::"m"(g_pattern[6])); break;
        case 7: __asm__ volatile("movdqu %0, %%xmm7" ::"m"(g_pattern[7])); break;
        case 8: __asm__ volatile("movdqu %0, %%xmm8" ::"m"(g_pattern[8])); break;
        case 9: __asm__ volatile("movdqu %0, %%xmm9" ::"m"(g_pattern[9])); break;
        case 10: __asm__ volatile("movdqu %0, %%xmm10" ::"m"(g_pattern[10])); break;
        case 11: __asm__ volatile("movdqu %0, %%xmm11" ::"m"(g_pattern[11])); break;
        case 12: __asm__ volatile("movdqu %0, %%xmm12" ::"m"(g_pattern[12])); break;
        case 13: __asm__ volatile("movdqu %0, %%xmm13" ::"m"(g_pattern[13])); break;
        case 14: __asm__ volatile("movdqu %0, %%xmm14" ::"m"(g_pattern[14])); break;
        default: __asm__ volatile("movdqu %0, %%xmm15" ::"m"(g_pattern[15])); break;
        }
    }
    set_mxcsr(kTestMxcsr);
}

void clobber_registers() {
    alignas(16) uint8_t junk[16];
    std::memset(junk, kClobberByte, sizeof(junk));
    for (int i = 0; i < 16; ++i) {
        switch (i) {
        case 0: __asm__ volatile("movdqu %0, %%xmm0" ::"m"(junk)); break;
        case 1: __asm__ volatile("movdqu %0, %%xmm1" ::"m"(junk)); break;
        case 2: __asm__ volatile("movdqu %0, %%xmm2" ::"m"(junk)); break;
        case 3: __asm__ volatile("movdqu %0, %%xmm3" ::"m"(junk)); break;
        case 4: __asm__ volatile("movdqu %0, %%xmm4" ::"m"(junk)); break;
        case 5: __asm__ volatile("movdqu %0, %%xmm5" ::"m"(junk)); break;
        case 6: __asm__ volatile("movdqu %0, %%xmm6" ::"m"(junk)); break;
        case 7: __asm__ volatile("movdqu %0, %%xmm7" ::"m"(junk)); break;
        case 8: __asm__ volatile("movdqu %0, %%xmm8" ::"m"(junk)); break;
        case 9: __asm__ volatile("movdqu %0, %%xmm9" ::"m"(junk)); break;
        case 10: __asm__ volatile("movdqu %0, %%xmm10" ::"m"(junk)); break;
        case 11: __asm__ volatile("movdqu %0, %%xmm11" ::"m"(junk)); break;
        case 12: __asm__ volatile("movdqu %0, %%xmm12" ::"m"(junk)); break;
        case 13: __asm__ volatile("movdqu %0, %%xmm13" ::"m"(junk)); break;
        case 14: __asm__ volatile("movdqu %0, %%xmm14" ::"m"(junk)); break;
        default: __asm__ volatile("movdqu %0, %%xmm15" ::"m"(junk)); break;
        }
    }
    set_mxcsr(kClobberMxcsr);
}

void check_patterns() {
    alignas(16) uint8_t got[16][16];
#define READ_XMM(index, reg) __asm__ volatile("movdqu %%xmm" #reg ", %0" : "=m"(got[index]))
    READ_XMM(0, 0); READ_XMM(1, 1); READ_XMM(2, 2); READ_XMM(3, 3);
    READ_XMM(4, 4); READ_XMM(5, 5); READ_XMM(6, 6); READ_XMM(7, 7);
    READ_XMM(8, 8); READ_XMM(9, 9); READ_XMM(10, 10); READ_XMM(11, 11);
    READ_XMM(12, 12); READ_XMM(13, 13); READ_XMM(14, 14); READ_XMM(15, 15);
#undef READ_XMM
    for (int i = 0; i < 16; ++i) {
        if (std::memcmp(got[i], g_pattern[i], 16) != 0) {
            std::fprintf(stderr, "xmm%d not preserved across ctx_switch\n", i);
            g_failures++;
        }
    }
    g_seen_mxcsr = get_mxcsr();
    if (g_seen_mxcsr != kTestMxcsr) {
        std::fprintf(stderr, "MXCSR not preserved: got 0x%08x want 0x%08x\n",
                     g_seen_mxcsr, kTestMxcsr);
        g_failures++;
    }
}

void run_context_a() {
    load_patterns();
    g_which = 1;
    ctx_switch(&g_a_saved, g_b_sp);
    check_patterns();
    ctx_switch(&g_b_saved, g_main_sp);
    for (;;) {
    }
}

void run_context_b() {
    clobber_registers();
    g_which = 0;
    ctx_switch(&g_b_saved, g_a_saved);
    for (;;) {
    }
}

} // namespace

extern "C" void sched_session_main(void) {
    if (g_which == 0)
        run_context_a();
    else
        run_context_b();
    for (;;) {
    }
}

int main() {
    for (int i = 0; i < 16; ++i) {
        for (int byte = 0; byte < 16; ++byte)
            g_pattern[i][byte] = static_cast<uint8_t>(i * 16 + byte + 1);
    }
    g_which = 0;
    g_a_sp = ctx_make(g_stack_a, kStackBytes);
    g_b_sp = ctx_make(g_stack_b, kStackBytes);
    /* Enter A the way the scheduler enters a session: adopt its frame. */
    ctx_switch(&g_main_sp, g_a_sp);
    if (g_failures) {
        std::printf("ctx fpu preservation: FAIL (%d)\n", g_failures);
        return 1;
    }
    std::printf("ctx fpu preservation: PASS (xmm0-15 + MXCSR across ctx_switch)\n");
    return 0;
}
