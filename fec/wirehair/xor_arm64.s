//go:build wirehairsimd && arm64

#include "textflag.h"

TEXT ·xorBytesInplaceAsm(SB), NOSPLIT, $0-24
	MOVD dst+0(FP), R0
	MOVD src+8(FP), R2
	MOVD n+16(FP), R1

loop64_inplace:
	CMP $64, R1
	BLT loop16_inplace
	MOVD R0, R3
	VLD1.P 64(R0), [V0.B16, V1.B16, V2.B16, V3.B16]
	VLD1.P 64(R2), [V4.B16, V5.B16, V6.B16, V7.B16]
	VEOR V4.B16, V0.B16, V0.B16
	VEOR V5.B16, V1.B16, V1.B16
	VEOR V6.B16, V2.B16, V2.B16
	VEOR V7.B16, V3.B16, V3.B16
	VST1.P [V0.B16, V1.B16, V2.B16, V3.B16], 64(R3)
	SUB $64, R1
	JMP loop64_inplace

loop16_inplace:
	CMP $16, R1
	BLT loop8_inplace
	MOVD R0, R3
	VLD1.P 16(R0), [V0.B16]
	VLD1.P 16(R2), [V1.B16]
	VEOR V1.B16, V0.B16, V0.B16
	VST1.P [V0.B16], 16(R3)
	SUB $16, R1
	JMP loop16_inplace

loop8_inplace:
	CMP $8, R1
	BLT loop1_inplace
	MOVD (R0), R4
	MOVD (R2), R5
	EOR R5, R4, R4
	MOVD R4, (R0)
	ADD $8, R0
	ADD $8, R2
	SUB $8, R1
	JMP loop8_inplace

loop1_inplace:
	CBZ R1, done_inplace
	MOVBU (R0), R4
	MOVBU (R2), R5
	EOR R5, R4, R4
	MOVB R4, (R0)
	ADD $1, R0
	ADD $1, R2
	SUB $1, R1
	JMP loop1_inplace

done_inplace:
	RET

TEXT ·xorBytesAcc3Asm(SB), NOSPLIT, $0-32
	MOVD dst+0(FP), R0
	MOVD x+8(FP), R2
	MOVD y+16(FP), R6
	MOVD n+24(FP), R1

loop64_acc3:
	CMP $64, R1
	BLT loop16_acc3
	MOVD R0, R3
	VLD1.P 64(R0), [V0.B16, V1.B16, V2.B16, V3.B16]
	VLD1.P 64(R2), [V4.B16, V5.B16, V6.B16, V7.B16]
	VLD1.P 64(R6), [V8.B16, V9.B16, V10.B16, V11.B16]
	VEOR V4.B16, V0.B16, V0.B16
	VEOR V5.B16, V1.B16, V1.B16
	VEOR V6.B16, V2.B16, V2.B16
	VEOR V7.B16, V3.B16, V3.B16
	VEOR V8.B16, V0.B16, V0.B16
	VEOR V9.B16, V1.B16, V1.B16
	VEOR V10.B16, V2.B16, V2.B16
	VEOR V11.B16, V3.B16, V3.B16
	VST1.P [V0.B16, V1.B16, V2.B16, V3.B16], 64(R3)
	SUB $64, R1
	JMP loop64_acc3

loop16_acc3:
	CMP $16, R1
	BLT loop8_acc3
	MOVD R0, R3
	VLD1.P 16(R0), [V0.B16]
	VLD1.P 16(R2), [V1.B16]
	VLD1.P 16(R6), [V2.B16]
	VEOR V1.B16, V0.B16, V0.B16
	VEOR V2.B16, V0.B16, V0.B16
	VST1.P [V0.B16], 16(R3)
	SUB $16, R1
	JMP loop16_acc3

loop8_acc3:
	CMP $8, R1
	BLT loop1_acc3
	MOVD (R0), R4
	MOVD (R2), R5
	MOVD (R6), R7
	EOR R5, R4, R4
	EOR R7, R4, R4
	MOVD R4, (R0)
	ADD $8, R0
	ADD $8, R2
	ADD $8, R6
	SUB $8, R1
	JMP loop8_acc3

loop1_acc3:
	CBZ R1, done_acc3
	MOVBU (R0), R4
	MOVBU (R2), R5
	MOVBU (R6), R7
	EOR R5, R4, R4
	EOR R7, R4, R4
	MOVB R4, (R0)
	ADD $1, R0
	ADD $1, R2
	ADD $1, R6
	SUB $1, R1
	JMP loop1_acc3

done_acc3:
	RET

TEXT ·xorBytesSetAsm(SB), NOSPLIT, $0-32
	MOVD dst+0(FP), R0
	MOVD x+8(FP), R2
	MOVD y+16(FP), R6
	MOVD n+24(FP), R1

loop64_set:
	CMP $64, R1
	BLT loop16_set
	MOVD R0, R3
	VLD1.P 64(R2), [V0.B16, V1.B16, V2.B16, V3.B16]
	VLD1.P 64(R6), [V4.B16, V5.B16, V6.B16, V7.B16]
	VEOR V4.B16, V0.B16, V0.B16
	VEOR V5.B16, V1.B16, V1.B16
	VEOR V6.B16, V2.B16, V2.B16
	VEOR V7.B16, V3.B16, V3.B16
	VST1.P [V0.B16, V1.B16, V2.B16, V3.B16], 64(R3)
	ADD $64, R0
	SUB $64, R1
	JMP loop64_set

loop16_set:
	CMP $16, R1
	BLT loop8_set
	MOVD R0, R3
	VLD1.P 16(R2), [V0.B16]
	VLD1.P 16(R6), [V1.B16]
	VEOR V1.B16, V0.B16, V0.B16
	VST1.P [V0.B16], 16(R3)
	ADD $16, R0
	SUB $16, R1
	JMP loop16_set

loop8_set:
	CMP $8, R1
	BLT loop1_set
	MOVD (R2), R4
	MOVD (R6), R5
	EOR R5, R4, R4
	MOVD R4, (R0)
	ADD $8, R0
	ADD $8, R2
	ADD $8, R6
	SUB $8, R1
	JMP loop8_set

loop1_set:
	CBZ R1, done_set
	MOVBU (R2), R4
	MOVBU (R6), R5
	EOR R5, R4, R4
	MOVB R4, (R0)
	ADD $1, R0
	ADD $1, R2
	ADD $1, R6
	SUB $1, R1
	JMP loop1_set

done_set:
	RET
