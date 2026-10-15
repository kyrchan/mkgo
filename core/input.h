#ifndef INPUT_H
#define INPUT_H
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

void input_init(void);
void input_poll(void); /* kernel ctx: serial -> focused ring */
int  input_recv(uint32_t sid, void *out, uint32_t cap);
int  input_focus_set(uint32_t caller_sid, int handle);

#ifdef __cplusplus
}
#endif
#endif
