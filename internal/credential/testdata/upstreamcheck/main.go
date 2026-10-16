package main

// Decrypts one enc:// credential using the pinned PicoClaw implementation and
// prints the plaintext. Invoked by internal/credential's compatibility test, not
// by Piccolo at runtime.
//
// The public entry point upstream offers for this is Resolver.Resolve: its
// resolveEncrypted helper is unexported. Going through Resolve is also the more
// faithful check, because it is the exact call PicoClaw's config loader makes on
// an api_keys value, so a pass here means the gateway would accept the same
// string.
//
// Ciphertext and key path arrive as argv so nothing secret is embedded here.

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/pkg/credential"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: upstreamcheck <enc://...> <ssh-key-path>")
		os.Exit(2)
	}
	ciphertext, keyPath := os.Args[1], os.Args[2]

	// Passphrase and key location are read the way the gateway reads them: from
	// the process environment. That is what makes this a compatibility check
	// rather than a reimplementation of one.
	os.Setenv(credential.PassphraseEnvVar, os.Getenv("COMPAT_PASSPHRASE"))
	os.Setenv(credential.SSHKeyPathEnvVar, keyPath)

	r := credential.NewResolver(filepath.Dir(keyPath))
	plain, err := r.Resolve(ciphertext)
	if err != nil {
		fmt.Fprintf(os.Stderr, "upstream rejected this ciphertext: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(plain)
}
