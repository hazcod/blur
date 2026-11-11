//go:build linux || darwin

#include "textflag.h"
#include "go_asm.h"

// This entry runs on the native stack provided by purego.SyscallN. It uses
// only caller-saved registers, preserving the assembler scratch R27 and LR.
// The first 256 bytes are target stack arguments, followed by saved state.
GLOBL ·arm64CallEntryAddr(SB), NOPTR|RODATA, $8
DATA ·arm64CallEntryAddr(SB)/8, $goexlib_cgo_call_arm64(SB)

TEXT goexlib_cgo_call_arm64(SB), NOSPLIT|NOFRAME, $0
	SUB $288, RSP
	STP (R27, R30), 256(RSP)
	MOVD R0, 272(RSP)
	MOVD R0, R9
	MOVD arm64CallFrame_stackSize(R9), R11
	CBZ R11, registers
	ADD $arm64CallFrame_stack, R9, R10
	MOVD RSP, R12
stack:
	MOVD.P 8(R10), R13
	MOVD.P R13, 8(R12)
	SUB $8, R11
	CBNZ R11, stack
registers:
	FMOVD (arm64CallFrame_floats+0*8)(R9), F0
	FMOVD (arm64CallFrame_floats+1*8)(R9), F1
	FMOVD (arm64CallFrame_floats+2*8)(R9), F2
	FMOVD (arm64CallFrame_floats+3*8)(R9), F3
	FMOVD (arm64CallFrame_floats+4*8)(R9), F4
	FMOVD (arm64CallFrame_floats+5*8)(R9), F5
	FMOVD (arm64CallFrame_floats+6*8)(R9), F6
	FMOVD (arm64CallFrame_floats+7*8)(R9), F7
	MOVD arm64CallFrame_x8(R9), R8
	MOVD arm64CallFrame_function(R9), R10
	LDP (arm64CallFrame_ints+0*8)(R9), (R0, R1)
	LDP (arm64CallFrame_ints+2*8)(R9), (R2, R3)
	LDP (arm64CallFrame_ints+4*8)(R9), (R4, R5)
	LDP (arm64CallFrame_ints+6*8)(R9), (R6, R7)
	BL (R10)
	MOVD 272(RSP), R9
	STP (R0, R1), arm64CallFrame_retInt(R9)
	FMOVD F0, (arm64CallFrame_retFloat+0*8)(R9)
	FMOVD F1, (arm64CallFrame_retFloat+1*8)(R9)
	FMOVD F2, (arm64CallFrame_retFloat+2*8)(R9)
	FMOVD F3, (arm64CallFrame_retFloat+3*8)(R9)
	LDP 256(RSP), (R27, R30)
	ADD $288, RSP
	RET
