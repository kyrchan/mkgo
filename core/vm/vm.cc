/* Scalar core: fetch/decode with a 256-entry jump table, MOV/LEA/SUB/JZ. */
#include "vm.h"
#include "../plat.h"
#include "../mm.h"

int vm_create(struct vm *v, const uint8_t *blob, uint64_t blob_len) {
    if (blob_len < sizeof(struct vbin_header))
        return -1;
    struct vbin_header h;
    __builtin_memcpy(&h, blob, sizeof(h));
    if (__builtin_memcmp(h.magic, VBIN_MAGIC, 4) != 0 || h.version != 1)
        return -1;
    uint64_t end = (uint64_t)h.data_off + h.data_len;
    if ((uint64_t)h.code_off + h.code_len > blob_len || end > blob_len ||
        h.entry >= h.code_len || (h.entry & 15))
        return -1;

    uint64_t mem_len = h.data_len;
    if (mem_len < VM_MIN_MEM)
        mem_len = VM_MIN_MEM;
    mem_len = (mem_len + 0xFFF) & ~0xFFFULL;
    uint8_t *mem = (uint8_t *)mm_alloc(mem_len, 4096);
    if (!mem)
        return -1;
    for (uint64_t i = 0; i < mem_len; i++)
        mem[i] = 0;
    __builtin_memcpy(mem, blob + h.data_off, h.data_len);

    for (int i = 0; i < 16; i++) {
        v->r[i] = 0;
        for (int j = 0; j < 32; j++)
            v->v[i][j] = 0;
    }
    v->code = blob + h.code_off;
    v->code_len = h.code_len;
    v->mem = mem;
    v->mem_len = mem_len;
    v->pc = h.entry;
    v->zf = 0;
    return 0;
}

static uint64_t width_bytes(uint8_t wsel) {
    static const uint64_t w[4] = {1, 2, 4, 8};
    return w[wsel & 3];
}

static int guest_load(struct vm *vm, uint64_t ea, uint8_t wsel, uint64_t *out) {
    if (ea + width_bytes(wsel) > vm->mem_len)
        return -1;
    uint64_t v = 0;
    for (uint64_t i = 0; i < width_bytes(wsel); i++)
        v |= (uint64_t)vm->mem[ea + i] << (8 * i);
    *out = v;
    return 0;
}

/* returns 1 when the store hit the console window */
static int guest_store(struct vm *vm, uint64_t ea, uint8_t wsel, uint64_t val) {
    if (ea + width_bytes(wsel) > vm->mem_len)
        return -1;
    if (ea == VM_CONSOLE_OFF) {
        console_puts("[vm] out ");
        console_hex64(val);
        console_puts("\n");
        return 1;
    }
    if (ea == VM_CONSOLE_OFF + 8) {
        console_putc((char)(val & 0xFF));
        return 1;
    }
    for (uint64_t i = 0; i < width_bytes(wsel); i++)
        vm->mem[ea + i] = (uint8_t)(val >> (8 * i));
    return 0;
}

void vec_load(void *p, void *dst);
void vec_store(void *p, const void *src);
void vec_bcast(uint64_t val, int szcls, void *dst);
void vec_sub(const void *a, const void *b, int szcls, void *dst);
int vec_cmpeq_all(const void *a, const void *b, int szcls, void *dst);

int vm_run(struct vm *vm) {
    /* C++ has no range designators; fill once (single-threaded kernel). */
    static const void *disp[256];
    if (!disp[OP_MOV]) {
        for (int i = 0; i < 256; i++)
            disp[i] = &&illegal;
        disp[OP_MOV] = &&L_mov;
        disp[OP_LEA] = &&L_lea;
        disp[OP_SUB] = &&L_sub;
        disp[OP_JZ] = &&L_jz;
        disp[OP_VMOVDQU] = &&L_vmovdqu;
        disp[OP_VPBROADCAST] = &&L_vpbcast;
        disp[OP_VPSUB] = &&L_vsub;
        disp[OP_VPCMPEQ] = &&L_vcmpeq;
        disp[OP_HALT] = &&L_halt;
    }
#define STEP() (vm->pc += sizeof(struct insn))
    const struct insn *code = (const struct insn *)vm->code;

    goto *disp[code[vm->pc / 16].op];

L_mov: {
    const struct insn *in = &code[vm->pc / 16];
    uint8_t mode = in->aux & 7;
    switch (mode) {
    case 0:
        if (in->dst)
            vm->r[in->dst] = in->imm;
        break;
    case 1:
        if (in->dst)
            vm->r[in->dst] = vm->r[in->src];
        break;
    case 2: {
        uint64_t v;
        if (guest_load(vm, vm->r[in->src] + in->imm, in->aux >> 4, &v))
            goto fault;
        if (in->dst)
            vm->r[in->dst] = v;
        break;
    }
    case 3: {
        int r = guest_store(vm, vm->r[in->dst] + in->imm, in->aux >> 4,
                            vm->r[in->src]);
        if (r < 0)
            goto fault;
        break;
    }
    }
    STEP();
    goto *disp[code[vm->pc / 16].op];
}

L_lea: {
    const struct insn *in = &code[vm->pc / 16];
    if (in->dst)
        vm->r[in->dst] = vm->r[in->src] + in->imm;
    STEP();
    goto *disp[code[vm->pc / 16].op];
}

L_sub: {
    const struct insn *in = &code[vm->pc / 16];
    uint64_t res;
    if ((in->aux & 7) == 0)
        res = vm->r[in->dst] - in->imm;
    else
        res = vm->r[in->dst] - vm->r[in->src];
    if (in->dst)
        vm->r[in->dst] = res;
    vm->zf = (res == 0);
    STEP();
    goto *disp[code[vm->pc / 16].op];
}

L_jz:
    vm->pc = vm->zf ? code[vm->pc / 16].imm : vm->pc + sizeof(struct insn);
    goto *disp[code[vm->pc / 16].op];

L_vmovdqu: {
    const struct insn *in = &code[vm->pc / 16];
    uint64_t ea = vm->r[in->src] + in->imm;
    if (ea + 32 > vm->mem_len)
        goto fault;
    if (in->aux & 0x80)
        vec_store(vm->mem + ea, vm->v[in->dst]);
    else
        vec_load(vm->mem + ea, vm->v[in->dst]);
    STEP();
    goto *disp[code[vm->pc / 16].op];
}

L_vpbcast: {
    const struct insn *in = &code[vm->pc / 16];
    vec_bcast(vm->r[in->src], in->aux & 3, vm->v[in->dst]);
    STEP();
    goto *disp[code[vm->pc / 16].op];
}

L_vsub: {
    const struct insn *in = &code[vm->pc / 16];
    vec_sub(vm->v[in->src], vm->v[(in->aux >> 4) & 15], in->aux & 3,
            vm->v[in->dst]);
    STEP();
    goto *disp[code[vm->pc / 16].op];
}

L_vcmpeq: {
    const struct insn *in = &code[vm->pc / 16];
    vm->zf = vec_cmpeq_all(vm->v[in->src], vm->v[(in->aux >> 4) & 15],
                           in->aux & 3, vm->v[in->dst]);
    STEP();
    goto *disp[code[vm->pc / 16].op];
}

L_halt:
    console_puts("[vm] halt at pc=");
    console_hex64(vm->pc);
    console_puts("\n");
    return 0;

fault:
    console_puts("[vm] MEMFAULT pc=");
    console_hex64(vm->pc);
    console_puts("\n");
    return -1;

illegal:
    console_puts("[vm] ILLOP pc=");
    console_hex64(vm->pc);
    console_puts("\n");
    return -2;
}
