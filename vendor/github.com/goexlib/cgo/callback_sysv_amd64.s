//go:build linux || darwin

#include "textflag.h"
#include "go_asm.h"
#include "funcdata.h"

// goexlib_cgo_callbackasm1 is the common callback thunk for the System V
// AMD64 ABI. It is entered from the generated CALL table
// (goexlib_cgo_callbackasm) with the C calling convention.
//
// Stack layout on entry:
//	0(SP)  return address into goexlib_cgo_callbackasm (used to compute the index)
//	8(SP)  return address of the C caller
//	16(SP) first stack argument
//
// It builds a cbFrame on the C stack, calls the dispatch trampoline
// (a C function pointer created with purego.NewCallback that ends up in
// cbDispatch) and restores the return value registers from the frame.
TEXT goexlib_cgo_callbackasm1(SB), NOSPLIT|NOFRAME, $0
	NO_LOCAL_POINTERS

	MOVQ 0(SP), AX // return address into the CALL table, used below for the index
	ADDQ $8, SP    // drop it: we return directly to the C caller

	// Reserve the cbFrame plus 16 bytes of padding. The padding keeps
	// RSP 16-byte aligned for the CALL below (RSP was congruent to 8
	// modulo 16 after the ADDQ).
	ADJSP $(cbFrame__size+16)

	// Save the argument registers into the frame.
	MOVSD X0, (cbFrame_floats+0*8)(SP)
	MOVSD X1, (cbFrame_floats+1*8)(SP)
	MOVSD X2, (cbFrame_floats+2*8)(SP)
	MOVSD X3, (cbFrame_floats+3*8)(SP)
	MOVSD X4, (cbFrame_floats+4*8)(SP)
	MOVSD X5, (cbFrame_floats+5*8)(SP)
	MOVSD X6, (cbFrame_floats+6*8)(SP)
	MOVSD X7, (cbFrame_floats+7*8)(SP)
	MOVQ  DI, (cbFrame_ints+0*8)(SP)
	MOVQ  SI, (cbFrame_ints+1*8)(SP)
	MOVQ  DX, (cbFrame_ints+2*8)(SP)
	MOVQ  CX, (cbFrame_ints+3*8)(SP)
	MOVQ  R8, (cbFrame_ints+4*8)(SP)
	MOVQ  R9, (cbFrame_ints+5*8)(SP)

	// Pointer to the stack arguments of the C caller
	// (past the frame, the padding and the caller return address).
	LEAQ (cbFrame__size+16+8)(SP), DX
	MOVQ DX, cbFrame_stack(SP)

	// Compute the callback index: the return address points past the
	// i-th CALL instruction (5 bytes each) of the table.
	MOVQ $goexlib_cgo_callbackasm(SB), DX
	SUBQ DX, AX
	XORL DX, DX
	MOVL $5, CX
	DIVL CX
	SUBQ $1, AX
	MOVQ AX, cbFrame_index(SP)

	// Call the dispatch trampoline: cbDispatch(&frame).
	MOVQ SP, DI
	MOVQ ·cbTrampoline(SB), R10
	XORL AX, AX // no vector arguments
	CALL R10

	// Restore the return value registers from the frame.
	MOVQ (cbFrame_retInt+0*8)(SP), AX
	MOVQ (cbFrame_retInt+1*8)(SP), DX
	MOVQ (cbFrame_retFlt+0*8)(SP), X0
	MOVQ (cbFrame_retFlt+1*8)(SP), X1

	ADJSP $-(cbFrame__size+16)
	RET
