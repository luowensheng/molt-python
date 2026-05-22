# 07-asm-kernel

**What this shows:** Calling hand-written assembly from Python using molt's kernel module system — with zero C or Cython boilerplate.

The demo implements two functions (`add`, `mul`) in portable GAS assembly (`.S`), compiles them to a native `.so` via `zig cc`, and imports the result directly in Python through a generated ctypes shim.

---

## Run it

```bash
molt sync      # compiles mymath.S → mymath.so, generates ctypes shim
molt run       # python main.py

# Output:
# add(3, 4)  = 7
# mul(6, 7)  = 42
```

---

## How it works

### 1. The assembly (`mymath.S`)

Portable GAS `.S` file — the capital extension tells `zig cc` to run the C preprocessor, enabling `#ifdef` for architecture detection:

```asm
#if defined(__aarch64__)
SYM(add):
    add  w0, w0, w1     // ARM64: args in w0/w1
    ret

#elif defined(__x86_64__)
SYM(add):
    movl %edi, %eax     // x86-64 SysV ABI: args in edi/esi
    addl %esi, %eax
    ret
#endif
```

Works on both Apple Silicon and x86-64 Linux with the same source file.

### 2. The manifest (`mymath.molt.toml`)

Declares the exported functions so molt can generate a ctypes shim:

```toml
[[fn]]
name    = "add"
args    = [{ name = "a", type = "i32" }, { name = "b", type = "i32" }]
returns = "i32"

[[fn]]
name    = "mul"
args    = [{ name = "a", type = "i32" }, { name = "b", type = "i32" }]
returns = "i32"
```

### 3. Auto-generated ctypes shim

`molt sync` compiles the `.S` file and writes a `mymath.py` shim that loads `mymath.so` and marshals Python ints to C `int32_t`:

```python
# mymath.py (auto-generated — do not edit)
import ctypes, os
_lib = ctypes.CDLL(os.path.join(os.path.dirname(__file__), "mymath.so"))
_lib.add.argtypes = [ctypes.c_int32, ctypes.c_int32]
_lib.add.restype  = ctypes.c_int32
def add(a, b): return _lib.add(a, b)
# ... mul likewise
```

### 4. Python usage

```python
import mymath
print(mymath.add(3, 4))   # 7
print(mymath.mul(6, 7))   # 42
```

---

## Supported types

| molt.toml type | C type | Python |
|---|---|---|
| `i32` | `int32_t` | `int` |
| `i64` | `int64_t` | `int` |
| `f32` | `float` | `float` |
| `f64` | `double` | `float` |
| `ptr` | `void*` | `ctypes.c_void_p` |

---

## Cross-compilation

Build for a different target without changing the source:

```bash
molt build --target linux_amd64   # cross-compile the kernel for Linux x86-64
```

molt invokes `zig cc` with the right `--target` triple — Zig's hermetic C toolchain handles the rest.

---

## Why zig cc?

`zig cc` is a single self-contained C/assembler toolchain that can cross-compile to any platform without installing separate toolchains. molt uses it as the default kernel compiler because it works out of the box on macOS, Linux, and Windows.

See [docs/kernel-modules.md](../../docs/kernel-modules.md) for the full kernel module reference.
