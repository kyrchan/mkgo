/* kern.h -- C header mirror of guests/lib (the guest "libc"),
 * encoding abi/ABI.md v1 exactly: message ports (§1), input/focus (§4),
 * kernel-owned service ports (§7). The kernel resolves these symbols
 * under import module "kernel" at instantiation (core/wasi_glue.cc);
 * sched_yield comes from the frozen WASI profile.
 *
 * Conventions: all integers little-endian; no NUL-terminated strings,
 * lengths are explicit; datagrams cap at KERN_MAX_MSG bytes.
 */
#ifndef KERN_H
#define KERN_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define KERN_ABI_VER 1
#define KERN_MAX_MSG 4096
#define KERN_MAX_NAME 15

/* ---- ABI §1 message ports ---- */
extern int32_t kern_port_create(const uint8_t *name, uint32_t name_len);
extern int32_t kern_port_bind(const uint8_t *name, uint32_t name_len);
extern int32_t kern_port_send(int32_t h, const uint8_t *buf, uint32_t len);
extern int32_t kern_port_recv(int32_t h, uint8_t *buf, uint32_t cap);

#define KERN_SEND_OK           0
#define KERN_SEND_ERR         (-1)
#define KERN_SEND_WOULD_BLOCK (-2)

/* Well-known names (§1 user services / §7 kernel-owned). */
static const char KERN_NAME_CONSOLE[] = "console";
static const char KERN_NAME_FS[]      = "fs";
static const char KERN_NAME_NET[]     = "net";
static const char KERN_NAME_LOGIN[]   = "login";
static const char KERN_NAME_SHELL[]   = "shell";
static const char KERN_NAME_REGISTRY[] = "registry";
static const char KERN_NAME_DEVMAN[]  = "devman";
static const char KERN_NAME_POWER[]   = "power";

/* ---- ABI §4 input/focus ---- */
extern int32_t kern_input_recv(uint8_t *buf, uint32_t cap);
extern void    kern_focus_set(int32_t h);

typedef struct {
    uint8_t  kind;      /* 1=key_down 2=key_up */
    uint8_t  mods;      /* bit0 shift bit1 ctrl bit2 alt */
    uint16_t codepoint; /* Unicode */
} kern_input_rec;       /* 4 bytes on the wire */

#define KERN_KEY_DOWN 1
#define KERN_KEY_UP   2
#define KERN_MOD_SHIFT 0x1
#define KERN_MOD_CTRL  0x2
#define KERN_MOD_ALT   0x4

/* ---- WASI (frozen profile) ---- */
extern void sched_yield(void);

/* ---- ABI §7 capability bits ---- */
#define KERN_CAP_KILL     (1ULL << 0)
#define KERN_CAP_DEVMAN   (1ULL << 1)
#define KERN_CAP_POWER    (1ULL << 2)
#define KERN_CAP_FOCUS    (1ULL << 3)
#define KERN_CAP_FS_ADMIN (1ULL << 4)
#define KERN_CAP_NET_ADMIN (1ULL << 5)
#define KERN_CAP_SPAWN    (1ULL << 6)

/* Session states (core/sched.cc enum st). */
#define KERN_ST_FREE     0
#define KERN_ST_RUNNABLE 1
#define KERN_ST_RUNNING  2
#define KERN_ST_ZOMBIE   3

/* §7 request framing {u16 op, u16 seq, payload}; replies echo both.
 * Kernel endpoints dispatch inline at send time and enqueue their reply
 * on the sending handle itself. */
#define KERN_REG_LIST  1
#define KERN_REG_CAPS  2
#define KERN_REG_KILL  3
#define KERN_REG_SPAWN 4
#define KERN_DVM_ENUM  1
#define KERN_PWR_REBOOT 1
#define KERN_PWR_OFF    2

/* LIST record: u32 sid, u32 uid, u8 state, char name[16] (25 B). */
typedef struct {
    uint32_t sid;
    uint32_t uid;
    uint8_t  state;
    char     name[16];
} kern_session_rec;

/* CAPS record: u32 cap_id, u64 rights (12 B). */
typedef struct {
    uint32_t cap_id;
    uint64_t rights;
} kern_cap_rec;

/* SPAWN request payload layout (v1): path ignored, module resolves from
 * /boot/modules by name. */
#define KERN_SPAWN_NAME_OFF 0
#define KERN_SPAWN_PATH_OFF 16
#define KERN_SPAWN_MASK_OFF 80
#define KERN_SPAWN_ARGC_OFF 84
#define KERN_SPAWN_ARGS_OFF 86
#define KERN_SPAWN_HDR_LEN  86

/* ---- little-endian helpers ---- */
static inline void kern_put16(uint8_t *p, uint16_t v) {
    p[0] = (uint8_t)v; p[1] = (uint8_t)(v >> 8);
}
static inline void kern_put32(uint8_t *p, uint32_t v) {
    for (int i = 0; i < 4; i++) p[i] = (uint8_t)(v >> (8 * i));
}
static inline uint16_t kern_get16(const uint8_t *p) {
    return (uint16_t)(p[0] | (p[1] << 8));
}
static inline uint32_t kern_get32(const uint8_t *p) {
    return (uint32_t)p[0] | ((uint32_t)p[1] << 8) |
           ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24);
}

#ifdef __cplusplus
}
#endif

#endif /* KERN_H */
