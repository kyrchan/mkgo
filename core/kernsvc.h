#ifndef KERNSVC_H
#define KERNSVC_H
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

// Audit level: 0 = deny-only, 1 = deny + use (default).
#ifndef KERN_AUDIT_LEVEL
#define KERN_AUDIT_LEVEL 1
#endif

// kernsvc_audit: emit an audit record to the log ring.
// Called from REQUIRE_CAP and the LOGIN minting check.
void kernsvc_audit(uint32_t sid, const char *op, const char *reason,
                   const char *target);

#ifdef __cplusplus
}
#endif

#endif
