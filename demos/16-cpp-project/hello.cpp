// hello.cpp — single-file C++17 program
// Run directly with: molt run hello.cpp [name]
// molt compiles via zig c++ -std=c++17 then execs the binary.
#include <iostream>
#include <string>

int main(int argc, char* argv[]) {
    std::string name = argc > 1 ? argv[1] : "World";
    std::cout << "Hello from C++17! 👋 " << name << "\n";
    return 0;
}
