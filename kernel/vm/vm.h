#ifndef VM_H
#define VM_H
#include <stdint.h>
#include "isa.h"

struct vm {
    uint64_t r[16];                       /* r0 is hardwired to zero  */
    uint8_t v[16][32] __attribute__((aligned(32)));
    uint64_t pc;
    int zf;                               /* set by SUB and VPCMPEQ   */
    const uint8_t *code;
    uint64_t code_len;
    uint8_t *mem;
    uint64_t mem_len;
};

int vm_create(struct vm *v, const uint8_t *blob, uint64_t blob_len);
int vm_run(struct vm *v);

#endif
