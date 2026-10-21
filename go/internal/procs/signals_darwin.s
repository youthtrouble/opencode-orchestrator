#include "textflag.h"

// The jump to libSystem's raise(3) that signalThisThread calls (signals_darwin.go); the same
// for amd64 and arm64.
TEXT libc_raise_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_raise(SB)

GLOBL	·libcRaiseTrampolineAddr(SB), RODATA, $8
DATA	·libcRaiseTrampolineAddr(SB)/8, $libc_raise_trampoline<>(SB)
