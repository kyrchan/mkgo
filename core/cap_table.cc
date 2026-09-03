// core/cap_table.cc -- capability bit table (single source of truth).
#include "cap_table.h"

const struct cap_entry kCapTable[13] = {
    { SCHED_CAP_KILL,     "CAP_KILL",     "SCHED_CAP_KILL",     "KILL",     true  },
    { SCHED_CAP_DEVMAN,   "CAP_DEVMAN",   "SCHED_CAP_DEVMAN",   "DEVMAN",   true  },
    { SCHED_CAP_POWER,    "CAP_POWER",    "SCHED_CAP_POWER",    "POWER",    true  },
    { SCHED_CAP_FOCUS,    "CAP_FOCUS",    "SCHED_CAP_FOCUS",    "FOCUS",    false },
    { SCHED_CAP_FSADM,    "CAP_FSADM",    "SCHED_CAP_FSADM",    "FSADM",    false },
    { SCHED_CAP_NETADM,   "CAP_NETADM",   "SCHED_CAP_NETADM",   "NETADM",   false },
    { SCHED_CAP_SPAWN,    "CAP_SPAWN",    "SCHED_CAP_SPAWN",    "SPAWN",    true  },
    { SCHED_CAP_CONF,     "CAP_CONF",     "SCHED_CAP_CONF",     "CONF",     false },
    { SCHED_CAP_PCI,      "CAP_PCI",      "SCHED_CAP_PCI",      "PCI",      true  },
    { SCHED_CAP_FB,       "CAP_FB",       "SCHED_CAP_FB",       "FB",       true  },
    { SCHED_CAP_DOORBELL, "CAP_DOORBELL", "SCHED_CAP_DOORBELL", "DOORBELL", false },
    { SCHED_CAP_VMWARE,   "CAP_VMWARE",   "SCHED_CAP_VMWARE",   "VMWARE",   false },
    { SCHED_CAP_PORTBIND, "CAP_PORTBIND", "SCHED_CAP_PORTBIND", "PORTBIND", true  },
};
const int kCapTableLen = 13;
