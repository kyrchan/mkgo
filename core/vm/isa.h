#ifndef VM_ISA_H
#define VM_ISA_H
#include <stdint.h>

/* Restricted guest ISA (fixed 16-byte instructions).
 *
 * Scalar core:  MOV, LEA, SUB, JZ        - fetch/decode, jump tables, control
 * Vector unit:  VMOVDQU, VPBROADCAST,
 *               VPSUB, VPCMPEQ           - bulk array math on 256-bit lanes
 * Supervisor:   HALT                     - stops the machine
 */

enum {
    OP_MOV = 1,
    OP_LEA,
    OP_SUB,
    OP_JZ,
    OP_VMOVDQU,
    OP_VPBROADCAST,
    OP_VPSUB,
    OP_VPCMPEQ,
    OP_HALT = 0xFF,
};

/* aux field layout:
 *   MOV:          mode = aux & 7   (0 imm->reg, 1 reg->reg, 2 mem->reg, 3 reg->mem)
 *                 width sel = (aux >> 4) & 3   -> {1,2,4,8} bytes
 *   SUB:          mode = aux & 7   (0 imm, 1 reg)
 *   VMOVDQU:      bit7 = store (else load); src = gpr base reg; imm = disp
 *   VPBROADCAST:  size class = aux & 3     (0=.b 1=.w 2=.d 3=.q)
 *   VPSUB/VPCMPEQ:size class = aux & 3; second vreg = (aux >> 4) & 15
 */
struct insn {
    uint8_t op;
    uint8_t dst;
    uint8_t src;
    uint8_t aux;
    uint32_t pad;
    uint64_t imm;
} __attribute__((packed));

/* .vbin program blob */
struct vbin_header {
    char magic[4];     /* "VBIN" */
    uint32_t version;  /* 1 */
    uint32_t entry;    /* byte offset into code section */
    uint32_t code_off;
    uint32_t code_len;
    uint32_t data_off;
    uint32_t data_len;
};

#define VBIN_MAGIC "VBIN"

/* guest console window inside the data segment */
#define VM_CONSOLE_OFF 0xF000u
#define VM_MIN_MEM     0x10000u

#endif
