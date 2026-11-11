#include "textflag.h"
#include "go_asm.h"
#include "funcdata.h"

// goexlib_cgo_callbackasm1 is the common callback thunk for the Microsoft
// x64 calling convention. It is entered from the generated CALL table
// (goexlib_cgo_callbackasm).
//
// Stack layout on entry:
//	0(SP)  return address into goexlib_cgo_callbackasm (used to compute the index)
//	8(SP)  return address of the C caller
//	16(SP) shadow space of the C caller (32 bytes)
//	48(SP) fifth argument
//
// It builds a cbFrame on the C stack, calls the dispatch trampoline
// (a C function pointer created with syscall.NewCallback that ends up in
// cbDispatch) and restores the return value registers from the frame.
TEXT goexlib_cgo_callbackasm1(SB), NOSPLIT|NOFRAME, $0
	NO_LOCAL_POINTERS

	MOVQ 0(SP), AX // return address into the CALL table, used below for the index
	ADDQ $8, SP    // drop it: we return directly to the C caller

	// Reserve 32 bytes of shadow space for the call below, the cbFrame
	// (at 32(SP)) and 16 bytes of padding. The padding keeps RSP 16-byte
	// aligned for the CALL below (RSP was congruent to 8 modulo 16 after
	// the ADDQ).
	ADJSP $(cbFrame__size+48)

	// Save the argument registers into the frame.
	MOVSD X0, (32+cbFrame_floats+0*8)(SP)
	MOVSD X1, (32+cbFrame_floats+1*8)(SP)
	MOVSD X2, (32+cbFrame_floats+2*8)(SP)
	MOVSD X3, (32+cbFrame_floats+3*8)(SP)
	MOVQ  CX, (32+cbFrame_ints+0*8)(SP)
	MOVQ  DX, (32+cbFrame_ints+1*8)(SP)
	MOVQ  R8, (32+cbFrame_ints+2*8)(SP)
	MOVQ  R9, (32+cbFrame_ints+3*8)(SP)

	// Pointer to the stack arguments of the C caller (past the frame,
	// the caller return address and the caller shadow space).
	LEAQ (cbFrame__size+48+8+32)(SP), DX
	MOVQ DX, (32+cbFrame_stack)(SP)

	// Compute the callback index: the return address points past the
	// i-th CALL instruction (5 bytes each) of the table.
	MOVQ $goexlib_cgo_callbackasm(SB), DX
	SUBQ DX, AX
	XORL DX, DX
	MOVL $5, CX
	DIVL CX
	SUBQ $1, AX
	MOVQ AX, (32+cbFrame_index)(SP)

	// Call the dispatch trampoline: cbDispatch(&frame).
	LEAQ 32(SP), CX
	MOVQ ·cbTrampoline(SB), R10
	CALL R10

	// Restore the return value registers from the frame.
	MOVQ (32+cbFrame_retInt+0*8)(SP), AX
	MOVQ (32+cbFrame_retFlt+0*8)(SP), X0

	ADJSP $-(cbFrame__size+48)
	RET
