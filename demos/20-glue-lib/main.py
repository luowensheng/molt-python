"""Demo 20 — Transport Glue (Go library import)

No local Go code written — molt imports Go's stdlib and third-party packages
directly and generates a server that exposes them to Python.
"""

import gosha256  # wraps crypto/sha256 from Go stdlib
import sha3      # wraps golang.org/x/crypto/sha3 (third-party)

data = b"Transport Glue demo - hashing from Go"

print("=== Transport Glue: Go library imports from Python ===\n")

sha256_digest = gosha256.sum256(data)
print(f"SHA-256 (Go stdlib crypto/sha256): {sha256_digest.hex()}")

sha3_256_digest = sha3.sum256(data)
print(f"SHA3-256 (golang.org/x/crypto):    {sha3_256_digest.hex()}")

sha3_512_digest = sha3.sum512(data)
print(f"SHA3-512 (golang.org/x/crypto):    {sha3_512_digest.hex()[:48]}...")

print(f"\n✓ Both Go packages called from Python with zero local Go code")
print(f"  SHA-256 ≠ SHA3-256: {sha256_digest.hex() != sha3_256_digest.hex()}")
