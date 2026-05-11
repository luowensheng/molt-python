// Portable GAS assembly that works on both ARM64 and x86-64.
// Uses .S (capital) so zig cc runs the C preprocessor on this file,
// enabling #ifdef for architecture detection and the SYM() macro for
// the macOS '_' symbol-name prefix.
#if defined(__APPLE__)
#  define SYM(x) _##x
#else
#  define SYM(x) x
#endif

    .text

#if defined(__aarch64__)
// ---------- AArch64 (Apple Silicon / Linux ARM64) ----------
// Calling convention: args in w0/w1 (32-bit), return value in w0.

    .global SYM(add)
SYM(add):
    add     w0, w0, w1
    ret

    .global SYM(mul)
SYM(mul):
    mul     w0, w0, w1
    ret

#elif defined(__x86_64__)
// ---------- x86-64 (Intel/AMD) ----------
// SysV ABI: args in edi/esi, return value in eax.

    .global SYM(add)
SYM(add):
    movl    %edi, %eax
    addl    %esi, %eax
    ret

    .global SYM(mul)
SYM(mul):
    movl    %edi, %eax
    imull   %esi, %eax
    ret

#else
#  error "Unsupported architecture"
#endif
