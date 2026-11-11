//go:build linux || darwin

#include "textflag.h"
#include "go_asm.h"
#include "funcdata.h"

// goexlib_cgo_callbackasm1 is the common callback thunk for the AAPCS64
// calling convention (linux and darwin). It is entered from the generated
// entry table (goexlib_cgo_callbackasm) with the callback index in R12.
// The stack arguments of the C caller start at the entry value of RSP.
//
// It builds a cbFrame on the C stack, calls the dispatch trampoline
// (a C function pointer created with purego.NewCallback that ends up in
// cbDispatch) and restores the return value registers from the frame.
//
// Frame layout (from RSP after the SUB):
//	0(RSP)   saved R27, R30
//	16(RSP)  cbFrame
TEXT goexlib_cgo_callbackasm1(SB), NOSPLIT|NOFRAME, $0
	NO_LOCAL_POINTERS

	SUB $(cbFrame__size+24), RSP

	// R27 must be preserved: the Go assembler uses it as scratch register
	// for the "MOVD ·cbTrampoline(SB), R9" global load below, but it is
	// callee-saved in the C ABI. R30 is the link register.
	STP (R27, R30), 0(RSP)

	// Save the argument registers into the frame.
	FMOVD F0, (16+cbFrame_floats+0*8)(RSP)
	FMOVD F1, (16+cbFrame_floats+1*8)(RSP)
	FMOVD F2, (16+cbFrame_floats+2*8)(RSP)
	FMOVD F3, (16+cbFrame_floats+3*8)(RSP)
	FMOVD F4, (16+cbFrame_floats+4*8)(RSP)
	FMOVD F5, (16+cbFrame_floats+5*8)(RSP)
	FMOVD F6, (16+cbFrame_floats+6*8)(RSP)
	FMOVD F7, (16+cbFrame_floats+7*8)(RSP)
	STP   (R0, R1), (16+cbFrame_ints+0*8)(RSP)
	STP   (R2, R3), (16+cbFrame_ints+2*8)(RSP)
	STP   (R4, R5), (16+cbFrame_ints+4*8)(RSP)
	STP   (R6, R7), (16+cbFrame_ints+6*8)(RSP)

	// X8 may hold the indirect result location pointer.
	MOVD R8, (16+cbFrame_x8)(RSP)

	// Pointer to the stack arguments of the C caller (the entry RSP).
	ADD  $(cbFrame__size+24), RSP, R13
	MOVD R13, (16+cbFrame_stack)(RSP)

	// Callback index from the entry table.
	MOVD R12, (16+cbFrame_index)(RSP)

	// Call the dispatch trampoline: cbDispatch(&frame).
	ADD  $16, RSP, R0
	MOVD ·cbTrampoline(SB), R9
	BL   (R9)

	// Restore the return value registers from the frame.
	LDP   (16+cbFrame_retInt)(RSP), (R0, R1)
	FMOVD (16+cbFrame_retFlt+0*8)(RSP), F0
	FMOVD (16+cbFrame_retFlt+1*8)(RSP), F1
	FMOVD (16+cbFrame_retFlt+2*8)(RSP), F2
	FMOVD (16+cbFrame_retFlt+3*8)(RSP), F3

	LDP 0(RSP), (R27, R30)
	ADD $(cbFrame__size+24), RSP
	RET
