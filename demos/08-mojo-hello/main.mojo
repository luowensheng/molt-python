# 08-mojo-hello / main.mojo
#
# Run with:  molt run main.mojo
#            molt mojo run main.mojo
#
# Demonstrates Mojo 1.0 basics: def/var, structs, traits, generics,
# operator overloading, and compile-time metaprogramming.

from math import sqrt


# ── 1. Typed functions ────────────────────────────────────────────────────────

def greet(name: String) -> String:
    return "Hello, " + name + "!"

def add(a: Int, b: Int) -> Int:
    return a + b

def clamp(val: Float64, lo: Float64, hi: Float64) -> Float64:
    if val < lo:
        return lo
    if val > hi:
        return hi
    return val


# ── 2. Struct with custom init and methods ────────────────────────────────────

struct Point(Copyable, Writable):
    var x: Float64
    var y: Float64

    def __init__(out self, x: Float64, y: Float64):
        self.x = x
        self.y = y

    def distance_to(self, other: Point) -> Float64:
        var dx = self.x - other.x
        var dy = self.y - other.y
        return sqrt(dx * dx + dy * dy)

    def write_to(self, mut writer: Some[Writer]):
        writer.write("Point(", self.x, ", ", self.y, ")")


# ── 3. Trait + generic function ───────────────────────────────────────────────

trait Describable:
    def describe(self) -> String: ...

@fieldwise_init
struct Circle(Describable):
    var radius: Float64

    def describe(self) -> String:
        return "Circle with radius " + String(self.radius)

@fieldwise_init
struct Rectangle(Describable):
    var width: Float64
    var height: Float64

    def describe(self) -> String:
        return "Rectangle " + String(self.width) + "x" + String(self.height)

def print_shape[T: Describable](shape: T):
    print(shape.describe())


# ── 4. Operator overloading: Complex numbers ──────────────────────────────────

struct Complex(Copyable, Writable):
    var re: Float64
    var im: Float64

    def __init__(out self, re: Float64, im: Float64 = 0.0):
        self.re = re
        self.im = im

    def __add__(self, rhs: Self) -> Self:
        return Self(self.re + rhs.re, self.im + rhs.im)

    def __mul__(self, rhs: Self) -> Self:
        return Self(
            self.re * rhs.re - self.im * rhs.im,
            self.re * rhs.im + self.im * rhs.re,
        )

    def norm(self) -> Float64:
        return sqrt(self.re * self.re + self.im * self.im)

    def write_to(self, mut writer: Some[Writer]):
        if self.im >= 0:
            writer.write(self.re, "+", self.im, "i")
        else:
            writer.write(self.re, self.im, "i")


# ── 5. Compile-time unrolling ─────────────────────────────────────────────────

def print_n_times[n: Int](msg: String):
    comptime for _ in range(n):
        print(msg)


# ── main ──────────────────────────────────────────────────────────────────────

def main():
    print("=== 1. Functions ===")
    print(greet("Mojo"))
    print("3 + 7 =", add(3, 7))
    print("clamp(2.5, 0, 1) =", clamp(2.5, 0.0, 1.0))

    print("\n=== 2. Struct ===")
    var a = Point(0.0, 0.0)
    var b = Point(3.0, 4.0)
    print(a)
    print(b)
    print("distance:", a.distance_to(b))   # 5.0

    print("\n=== 3. Traits + Generics ===")
    print_shape(Circle(radius=7.0))
    print_shape(Rectangle(width=4.0, height=9.0))

    print("\n=== 4. Operator Overloading ===")
    var c1 = Complex(1.0, 2.0)
    var c2 = Complex(3.0, -1.0)
    var c_sum = c1 + c2
    var c_prod = c1 * c2
    print("c1 =", c1)
    print("c2 =", c2)
    print("c1 + c2 =", c_sum)
    print("c1 * c2 =", c_prod)
    print("|c1| =", c1.norm())

    print("\n=== 5. Compile-time unrolling ===")
    print_n_times[3]("Hello from comptime!")

    print("\n=== 6. Variables and type inference ===")
    var counter = 0
    var total = 0.0
    for i in range(1, 11):
        counter += 1
        total += Float64(i)
    print("sum(1..10) =", total, "count =", counter)
    print("average =", total / Float64(counter))
