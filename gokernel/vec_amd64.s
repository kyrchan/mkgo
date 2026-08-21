#include "textflag.h"

// Vector accelerator: guest vector ops lower 1:1 onto AVX2.
// All operands are pointers to 32-byte slots.

TEXT ·vecLoad(SB), NOSPLIT, $0-16
	MOVQ	src+0(FP), SI
	MOVQ	dst+8(FP), DI
	VMOVDQU	(SI), Y0
	VMOVDQU	Y0, (DI)
	RET

TEXT ·vecStore(SB), NOSPLIT, $0-16
	MOVQ	src+0(FP), SI
	MOVQ	dst+8(FP), DI
	VMOVDQU	(SI), Y0
	VMOVDQU	Y0, (DI)
	RET

TEXT ·vecBcast(SB), NOSPLIT, $16-24
	MOVQ	val+0(FP), AX
	MOVQ	cls+8(FP), CX
	MOVQ	dst+16(FP), DI
	SUBQ	$16, SP			// scratch slot (keeps 16B alignment)
	CMPQ	CX, $3
	JE	q
	CMPQ	CX, $2
	JE	d
	CMPQ	CX, $1
	JE	w
	MOVB	AL, BL
	MOVQ	BX, (SP)
	VPBROADCASTB	(SP), Y0
	JMP	store
w:
	MOVW	AX, BX
	MOVQ	BX, (SP)
	VPBROADCASTW	(SP), Y0
	JMP	store
d:
	MOVL	AX, BX
	MOVQ	BX, (SP)
	VPBROADCASTD	(SP), Y0
	JMP	store
q:
	MOVQ	AX, (SP)
	VPBROADCASTQ	(SP), Y0
store:
	VMOVDQU	Y0, (DI)
	ADDQ	$16, SP
	RET

TEXT ·vecSub(SB), NOSPLIT, $0-32
	MOVQ	a+0(FP), SI
	MOVQ	b+8(FP), DX
	MOVQ	cls+16(FP), CX
	MOVQ	dst+24(FP), DI
	VMOVDQU	(SI), Y0
	VMOVDQU	(DX), Y1
	CMPQ	CX, $3
	JE	qsub
	CMPQ	CX, $2
	JE	dsub
	CMPQ	CX, $1
	JE	wsub
	VPSUBB	Y1, Y0, Y2
	JMP	subdone
wsub:
	VPSUBW	Y1, Y0, Y2
	JMP	subdone
dsub:
	VPSUBD	Y1, Y0, Y2
	JMP	subdone
qsub:
	VPSUBQ	Y1, Y0, Y2
subdone:
	VMOVDQU	Y2, (DI)
	RET

// returns true when every lane compared equal (mask all ones)
TEXT ·vecCmpEqAll(SB), NOSPLIT, $0-40
	MOVQ	a+0(FP), SI
	MOVQ	b+8(FP), DX
	MOVQ	cls+16(FP), CX
	MOVQ	dst+24(FP), DI
	VMOVDQU	(SI), Y0
	VMOVDQU	(DX), Y1
	CMPQ	CX, $3
	JE	qeq
	CMPQ	CX, $2
	JE	deq
	CMPQ	CX, $1
	JE	weq
	VPCMPEQB	Y1, Y0, Y2
	JMP	eqdone
weq:
	VPCMPEQW	Y1, Y0, Y2
	JMP	eqdone
deq:
	VPCMPEQD	Y1, Y0, Y2
	JMP	eqdone
qeq:
	VPCMPEQQ	Y1, Y0, Y2
eqdone:
	VMOVDQU	Y2, (DI)
	MOVQ	(DI), AX
	ORQ	8(DI), AX
	ORQ	16(DI), AX
	ORQ	24(DI), AX
	INCQ	AX			// all-ones + 1 == 0
	SETEQ	AL
	MOVB	AL, ret+32(FP)
	RET
