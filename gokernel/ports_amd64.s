#include "textflag.h"

// console + cpu primitives for the Go kernel, Plan 9 assembly.

TEXT ·putc(SB), NOSPLIT, $0-1
	MOVB	b+0(FP), AL
	MOVQ	$0x3FD, DX
wait:
	INB
	TESTB	$0x20, AL
	JZ	wait
	MOVQ	$0x3F8, DX
	MOVB	b+0(FP), AL
	OUTB
	RET

TEXT ·halt(SB), NOSPLIT, $0-0
	CLI
loop:
	HLT
	JMP	loop

// enable SSE/AVX state: CR0.MP=1 EM=0, CR4.OSFXSR|OSXSAVE, XCR0=x87|SSE|YMM
TEXT ·enableAVX2(SB), NOSPLIT, $0-0
	MOVQ	CR0, AX
	ANDQ	$~4, AX			// clear EM
	ORQ	$2, AX			// set MP
	MOVQ	AX, CR0
	MOVQ	CR4, AX
	ORQ	$(1<<9), AX		// OSFXSR
	ORQ	$(1<<18), AX		// OSXSAVE
	MOVQ	AX, CR4
	XORL	CX, CX
	XGETBV
	ORQ	$7, AX
	XSETBV
	RET

TEXT ·cpuidAvx2(SB), NOSPLIT, $0-1
	XORL	AX, AX
	CPUID
	CMPL	AX, $7
	JB	no
	MOVL	$7, AX
	XORL	CX, CX
	CPUID
	ANDL	$32, BX		// AVX2 bit in EBX
	SETNE	AL
	MOVB	AL, ret+0(FP)
	RET
no:
	MOVB	$0, ret+0(FP)
	RET
