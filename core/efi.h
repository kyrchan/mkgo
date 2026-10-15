#ifndef EFI_H
#define EFI_H
#include <stdint.h>
#include <stddef.h>

typedef uint64_t EFI_STATUS;
typedef void    *EFI_HANDLE;
typedef uint16_t CHAR16;
typedef uint64_t UINTN;
typedef uint32_t UINT32;

/* UEFI x64 firmware speaks the MS ABI; our C is SysV. */
#define EFI_MSABI __attribute__((ms_abi))

typedef EFI_STATUS (__attribute__((ms_abi)) *msapi1_t)(UINTN);
typedef EFI_STATUS (__attribute__((ms_abi)) *msapi2_t)(UINTN, UINTN);
typedef EFI_STATUS (__attribute__((ms_abi)) *msapi3_t)(UINTN, UINTN, UINTN);
typedef EFI_STATUS (__attribute__((ms_abi)) *msapi4_t)(UINTN, UINTN, UINTN, UINTN);
typedef EFI_STATUS (__attribute__((ms_abi)) *msapi5_t)(UINTN, UINTN, UINTN, UINTN, UINTN);

#define FW1(fn, a)           ((msapi1_t)(fn))(a)
#define FW2(fn, a, b)        ((msapi2_t)(fn))(a, b)
#define FW3(fn, a, b, c)     ((msapi3_t)(fn))(a, b, c)
#define FW4(fn, a, b, c, d)  ((msapi4_t)(fn))(a, b, c, d)
#define FW5(fn, a, b, c, d, e) ((msapi5_t)(fn))(a, b, c, d, e)

#define EFI_SUCCESS           ((EFI_STATUS)0)
#define EFI_INVALID_PARAMETER ((EFI_STATUS)0x8000000000000002ULL)
#define EFI_UNSUPPORTED       ((EFI_STATUS)0x8000000000000003ULL)
#define EFI_BUFFER_TOO_SMALL  ((EFI_STATUS)0x8000000000000005ULL)
#define EFI_NOT_FOUND         ((EFI_STATUS)0x800000000000000EULL)
#define EFI_IS_ERROR(x)       ((int)(((x) >> 63) & 1))

#define EfiReservedMemoryType      0
#define EfiLoaderCode              1
#define EfiLoaderData              2
#define EfiBootServicesCode        3
#define EfiBootServicesData        4
#define EfiRuntimeCode             5
#define EfiRuntimeData             6
#define EfiConventionalMemory      7
#define EfiUnusableMemory          8
#define EfiACPIReclaimMemory       9
#define EfiACPIMemoryNVS          10
#define EfiMemoryMappedIO         11
#define EfiMemoryMappedIOPortSpace 12
#define EfiPalCode                13

typedef struct {
    uint32_t Type;
    uint32_t Pad;
    uint64_t PhysicalStart;
    uint64_t VirtualStart;
    uint64_t NumberOfPages;
    uint64_t Attribute;
} EFI_MEMORY_DESCRIPTOR;

typedef struct {
    void *Reset;
    void *OutputString;
    void *TestString;
    void *QueryMode;
    void *SetMode;
    void *ClearScreen;
    void *SetAttribute;
} EFI_SIMPLE_TEXT_OUTPUT_PROTOCOL;

typedef struct {
    uint64_t Revision;
    void *Open;
    void *Close;
    void *Delete;
    void *Read;
    void *Write;
    void *GetPosition;
    void *SetPosition;
    void *GetInfo;
    void *SetInfo;
    void *Flush;
} EFI_FILE_PROTOCOL;

typedef struct {
    uint64_t Revision;
    void *OpenVolume;
} EFI_SIMPLE_FILE_SYSTEM_PROTOCOL;

typedef struct { uint32_t a; uint16_t b; uint16_t c; uint8_t d[8]; } EFI_GUID;

#define EFI_SIMPLE_FILE_SYSTEM_PROTOCOL_GUID \
    ((EFI_GUID){0x964e5b22u,0x6459,0x11d2,{0x8e,0x39,0x00,0xa0,0xc9,0x69,0x72,0x3b}})

/* EFI Graphics Output Protocol (GOP) — provides the linear framebuffer that
 * the firmware (OVMF) scans out to the display. We locate it before
 * ExitBootServices and use its framebuffer as our scanout target so guest
 * renders reach the single firmware display window. */
#define EFI_GRAPHICS_OUTPUT_PROTOCOL_GUID \
    ((EFI_GUID){0x9042a9deu,0x23dc,0x4a38,{0x96,0xfb,0x7a,0xde,0xd0,0x80,0x51,0x6a}})

/* ACPI RSDP (Root System Description Pointer) GUID. Used to locate the
 * ACPI tables so we can find the MADT for AP bring-up. */
#define EFI_ACPI_TABLE_GUID \
    ((EFI_GUID){0x8868e871u,0xe4f1,0x11d3,{0xbc,0x22,0x00,0x80,0xc7,0x3c,0x88,0x81}})

typedef struct {
    uint32_t MaxMode;
    uint32_t Mode;
    void *ModeInfo;      /* EFI_GRAPHICS_OUTPUT_MODE_INFORMATION * */
    UINTN InfoSize;
    uint64_t FrameBufferBase;
    uint64_t FrameBufferSize;
} EFI_GOP_MODE;

/* EFI Configuration Table entry: a GUID + a pointer to the table. */
typedef struct {
    EFI_GUID guid;
    void *table;
} EFI_CONFIG_TABLE_ENTRY;

/* ACPI RSDP (Root System Description Pointer). Physical address 0xE0000
 * or in the EBDA; the EFI Configuration Table gives us the canonical
 * one. The RSDP points at the RSDT/XSDT which contains the MADT. */
typedef struct {
    char   signature[8];   /* "RSD PTR " */
    uint8_t cksum;
    char   oemid[6];
    uint8_t revision;
    uint32_t rsdt;         /* physical address of RSDT (32-bit) */
    uint64_t xsdt;         /* physical address of XSDT (64-bit) */
    uint8_t ext_cksum;
    uint8_t reserved[3];
} EFI_RSDP;

typedef struct {
    void *QueryMode;
    void *SetMode;
    void *Blt;
    EFI_GOP_MODE *Mode;
} EFI_GRAPHICS_OUTPUT_PROTOCOL;

typedef struct {
    uint64_t Signature;
    uint32_t Revision;
    uint32_t HeaderSize;
    uint32_t CRC32;
    uint32_t Reserved;
} EFI_TABLE_HEADER;

typedef struct {
    EFI_TABLE_HEADER Hdr;
    void *RaiseTPL;
    void *RestoreTPL;
    void *AllocatePages;
    void *FreePages;
    void *GetMemoryMap;
    void *AllocatePool;
    void *FreePool;
    void *CreateEvent;
    void *SetTimer;
    void *WaitForEvent;
    void *SignalEvent;
    void *CloseEvent;
    void *CheckEvent;
    void *InstallProtocolInterface;
    void *ReinstallProtocolInterface;
    void *UninstallProtocolInterface;
    void *HandleProtocol;
    void *Reserved;
    void *RegisterProtocolNotify;
    void *LocateHandle;
    void *LocateDevicePath;
    void *InstallConfigurationTable;
    void *LoadImage;
    void *StartImage;
    void *Exit;
    void *UnloadImage;
    void *ExitBootServices;
    void *GetNextMonotonicCount;
    void *Stall;
    void *SetWatchdogTimer;
    void *ConnectController;
    void *DisconnectController;
    void *OpenProtocol;
    void *CloseProtocol;
    void *OpenProtocolInformation;
    void *ProtocolsPerHandle;
    void *LocateHandleBuffer;
    void *LocateProtocol;
    void *InstallMultipleProtocolInterfaces;
    void *UninstallMultipleProtocolInterfaces;
    void *CalculateCrc32;
    void *CopyMem;
    void *SetMem;
    void *CreateEventEx;
} EFI_BOOT_SERVICES;

typedef struct {
    EFI_TABLE_HEADER Hdr;
    const CHAR16 *FirmwareVendor;
    uint32_t FirmwareRevision;
    uint32_t _pad;
    EFI_HANDLE ConsoleInHandle;
    void *ConIn;
    EFI_HANDLE ConsoleOutHandle;
    EFI_SIMPLE_TEXT_OUTPUT_PROTOCOL *ConOut;
    EFI_HANDLE StandardErrorHandle;
    void *StdErr;
    void *RuntimeServices;
    EFI_BOOT_SERVICES *BootServices;
    UINTN NumberOfTableEntries;
    void *ConfigurationTable;
} EFI_SYSTEM_TABLE;

#endif
