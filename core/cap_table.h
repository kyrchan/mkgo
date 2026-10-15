// core/cap_table.h -- single source of truth for capability bits.
#pragma once
#include <stdbool.h>
#include <stdint.h>
#include "sched.h"

struct cap_entry {
    uint64_t     bit;
    const char  *name;
    const char  *c_name;
    const char  *audit_op;
    bool         log_on_use;
};

extern const struct cap_entry kCapTable[13];
extern const int kCapTableLen;
