"""hello-molt: sample application demonstrating molt distribution."""
import sys


def main() -> None:
    print("Hello from molt!")
    print(f"Python: {sys.version}")
    try:
        import requests  # type: ignore[import]
        print(f"requests: {requests.__version__}")
    except ImportError:
        print("requests: not installed (run with full dependencies)")


if __name__ == "__main__":
    main()
