/* hello.c — single-file C program
 * Run directly with: molt run hello.c [name]
 * molt compiles it via zig cc then execs the binary — zero setup.
 */
#include <stdio.h>

int main(int argc, char *argv[]) {
    const char *name = argc > 1 ? argv[1] : "World";
    printf("Hello from C! 👋 %s\n", name);
    return 0;
}
