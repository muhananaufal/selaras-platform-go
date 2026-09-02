// Command keygen mencetak sepasang kunci Ed25519 untuk penandatanganan token.
//
// Ia ada supaya kunci penandatanganan dan kunci verifikasi selalu berpasangan.
// Membuat keduanya terpisah - dua perintah, dua salinan-tempel - menghasilkan
// pasangan yang tidak cocok, dan gejalanya adalah setiap token ditolak tanpa
// satu pun pesan yang menyebut kuncinya.
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

	// Yang dicetak adalah seed 32 byte, bukan kunci privat 64 byte: seed
	// itulah yang benar-benar rahasia, dan 32 byte sisanya adalah kunci
	// publik yang bisa diturunkan darinya.
	fmt.Printf("JWT_SIGNING_KEY=%s\n", base64.StdEncoding.EncodeToString(private.Seed()))
	fmt.Printf("JWT_VERIFY_KEY=%s\n", base64.StdEncoding.EncodeToString(public))
}
