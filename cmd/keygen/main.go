// Command keygen prints an Ed25519 key pair for token signing.
//
// It exists so the signing key and the verification key are always a pair.
// Producing the two separately - two commands, two copy-pastes - yields a
// mismatched pair, and the symptom is every token being refused without a
// single message naming the key.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
)

func main() {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintln(os.Stderr, "generating key:", err)
		os.Exit(1)
	}

	// What is printed is the 32-byte seed, not the 64-byte private key: the
	// seed is what is actually secret, and the remaining 32 bytes are the
	// public key that can be derived from it.
	fmt.Printf("JWT_SIGNING_KEY=%s\n", base64.StdEncoding.EncodeToString(private.Seed()))
	fmt.Printf("JWT_VERIFY_KEY=%s\n", base64.StdEncoding.EncodeToString(public))
}
