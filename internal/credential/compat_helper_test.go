package credential

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The pinned PicoClaw release Piccolo's format must match. Referenced only by
// the compatibility checker below, never imported into Piccolo's build graph.
const (
	upstreamModule   = "github.com/sipeed/picoclaw"
	upstreamVersion  = "v0.3.1"
	upstreamCredPath = "pkg/credential"
)

// upstreamModuleAvailable reports whether the pinned module is in the local Go
// module cache. The checker runs offline against the cache, so this is an
// environment check rather than a network one.
func upstreamModuleAvailable() bool {
	dir, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		return false
	}
	root := filepath.Join(strings.TrimSpace(string(dir)), "cache", "download",
		strings.Replace(upstreamModule, "/", "/", 1))
	// Module cache layout: <mod>/@v/<version>.zip
	if _, err := os.Stat(filepath.Join(root, "@v", upstreamVersion+".zip")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(root, "@v", upstreamVersion+".mod")); err != nil {
		return false
	}
	return true
}

// runCompatCheck hands a ciphertext to a throwaway program that decrypts it with
// the pinned upstream implementation. The program is built in its own module
// under testdata so Piccolo's go.mod never gains a PicoClaw dependency: that
// separation is design decision D1, and importing upstream into the test binary
// would quietly undo it.
//
// It is run with the module cache as the proxy so no network is needed.
func runCompatCheck(ciphertext, keyPath, passphrase string) (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	checker := filepath.Join(dir, "testdata", "upstreamcheck")

	// "go run" writes its compiled binary into the package directory, which would
	// leave an untracked artifact inside the repo on every test run. Point GOCACHE
	// somewhere transient and run by file argument so nothing lands in testdata.
	cmd := exec.Command("go", "run", "main.go", ciphertext, keyPath)
	cmd.Dir = checker
	cmd.Env = append(os.Environ(),
		"GOFLAGS=-mod=mod",
		"GOSUMDB=off",
		"GOPRIVATE=*",
		"GONOSUMDB=*",
		"GOPROXY=file://"+moduleCacheDownloadDir(),
		"COMPAT_PASSPHRASE="+passphrase,
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// moduleCacheDownloadDir locates the local module cache so the checker resolves
// the pinned module offline. GOPROXY=off would refuse a cache hit that has not
// been recorded as a proxy download, so the cache is served as a file:// proxy.
func moduleCacheDownloadDir() string {
	out, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		return ""
	}
	return filepath.Join(strings.TrimSpace(string(out)), "cache", "download")
}
