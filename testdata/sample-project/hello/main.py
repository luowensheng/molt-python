"""hello-pyexec: sample application demonstrating PyExec distribution."""
import sys


def main() -> None:
    print("Hello from PyExec!")
    print(f"Python: {sys.version}")
    try:
        import requests  # type: ignore[import]
        print(f"requests: {requests.__version__}")
    except ImportError:
        print("requests: not installed (run with full dependencies)")


if __name__ == "__main__":
    main()
