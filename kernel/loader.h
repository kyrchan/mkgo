#ifndef LOADER_H
#define LOADER_H
#include <stdint.h>
#include "efi.h"

/* Reads \vm\prog.vbin from the first SimpleFileSystem volume into an
 * EfiLoaderData pool buffer. Must run before ExitBootServices.
 * Returns 0 on success (buffer valid until EBS and beyond). */
int load_program(EFI_HANDLE image_handle, EFI_SYSTEM_TABLE *st,
                 void **out, uint64_t *out_len);

#endif
