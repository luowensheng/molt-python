import sys
import platform
import time


def greet(name: str) -> str:
    return f"Hello, {name}! 👋"


def show_info() -> None:
    print(f"Python  : {sys.version.split()[0]}")
    print(f"Platform: {platform.system()} {platform.machine()}")
    print(f"Time    : {time.strftime('%Y-%m-%d %H:%M:%S')}")


def main() -> None:
    name = sys.argv[1] if len(sys.argv) > 1 else "World"
    print(greet(name))
    print()
    show_info()


if __name__ == "__main__":
    main()
