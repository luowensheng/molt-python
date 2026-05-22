# 08-mojo-hello

The simplest Mojo demo: variables, typed functions, structs, traits, generics,
operator overloading, and compile-time metaprogramming — all run through molt with
zero manual environment setup.

## What this demo shows

- `molt add mojo` installs the Mojo compiler as a regular Python dependency
- `molt run main.mojo` dispatches to `mojo run` automatically by file extension —
  same ergonomics as `molt run main.py`
- `MOJO_PYTHON_LIBRARY` and `PYTHONPATH` are injected at sync time; no manual
  environment variable management
- Mojo 1.0 syntax: `def` (not `fn`), `var`, `@fieldwise_init`, `comptime for`

## Project layout

```
08-mojo-hello/
  main.mojo        entry point — covers all basic language features
  pyproject.toml   project metadata + molt tasks
  README.md
```

## Running

```bash
# First sync (downloads mojo compiler into the project environment)
molt sync

# Run the demo
molt run main.mojo

# Equivalent via molt mojo passthrough
molt mojo run main.mojo

# List tasks
molt task list
```

## Expected output

```
=== 1. Functions ===
Hello, Mojo!
3 + 7 = 10
clamp(2.5, 0, 1) = 1.0

=== 2. Struct ===
Point(0.0, 0.0)
Point(3.0, 4.0)
distance: 5.0

=== 3. Traits + Generics ===
Circle with radius 7.0
Rectangle 4.0x9.0

=== 4. Operator Overloading ===
c1 = 1.0+2.0i
c2 = 3.0-1.0i
c1 + c2 = 4.0+1.0i
c1 * c2 = 5.0+5.0i
|c1| = 2.23606797749979

=== 5. Compile-time unrolling ===
Hello from comptime!
Hello from comptime!
Hello from comptime!

=== 6. Variables and type inference ===
sum(1..10) = 55.0  count = 10
average = 5.5
```

## Mojo 1.0 features demonstrated

| Feature | Example |
|---|---|
| Typed function | `def greet(name: String) -> String` |
| Struct | `struct Point(Copyable, Writable)` |
| `@fieldwise_init` | Auto-generates `__init__` from field list |
| Trait | `trait Describable` + constraint `[T: Describable]` |
| Generic function | `def print_shape[T: Describable](shape: T)` |
| Operator overloading | `__add__`, `__mul__` on `Complex` |
| Compile-time loop | `comptime for _ in range(n)` |
| Type inference | `var counter = 0` infers `Int` |

## What's in pyproject.toml

```toml
[project]
dependencies = ["mojo>=1.0.0b1"]

[tool.molt.tasks]
run = "molt run main.mojo"
```

`molt run main.mojo` works because molt detects the `.mojo` extension and dispatches
to `mojo run` automatically, with `MOJO_PYTHON_LIBRARY` and `PYTHONPATH` already set.
