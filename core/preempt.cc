/* Phase 8: timer/preempt config. Cooperative scheduling is primary;
 * IRQ-driven preemption can be enabled via SETCONF preempt=1. */
#include <stdint.h>
#ifdef __cplusplus
extern "C" {
#endif
void conf_set_quantum_us(uint64_t us);
void conf_set_preempt(uint64_t on);
uint64_t time_ns(void);
#ifdef __cplusplus
}
#endif

static volatile uint32_t quantum_ticks = 5;
static volatile uint8_t preempt_on = 0;

void conf_set_quantum_us(uint64_t us) {
    if (us >= 100 && us <= 200000)
        quantum_ticks = (uint32_t)(us / 1000);
}
void conf_set_preempt(uint64_t on) { preempt_on = (uint8_t)(on & 1); }
uint64_t time_ns(void) { return 0; }
