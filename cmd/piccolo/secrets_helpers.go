package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/kimus/piccolo/internal/configdoc"
	"github.com/kimus/piccolo/internal/credential"
	"github.com/kimus/piccolo/internal/picoclaw"
)

// confirmDestructive asks a [y/N] question, defaulting to No. It mirrors image
// prune's gate rather than inventing a second one: an answer that is empty,
// unreadable, or anything other than yes means do nothing.
//
// The terminal requirement is checked on os.Stdin because that is the only place a
// passphrase or a confirmation answer can genuinely come from. `in` exists so the
// ANSWER PARSING is testable, not so a pipe can stand in for a terminal: an
// injected reader with the tty check removed would let a scripted run confirm
// itself, which is the opposite of what a destructive gate is for. Callers that
// mean "the user already said yes" pass --yes and never reach here.
func confirmDestructive(out io.Writer, in io.Reader, question string) (bool, error) {
	if !stdinIsTerminal() {
		return false, fmt.Errorf("%s needs confirmation and stdin is not a terminal — re-run with --yes once you have read the warning", question)
	}
	fmt.Fprintf(out, "%s [y/N] ", question)
	line, err := bufio.NewReader(in).ReadString('\n')
	return parseConfirmation(line, err), nil
}

// stdinIsTerminal reports whether an interactive answer is even possible.
func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// parseConfirmation is the answer rule split out so the default-No behaviour is
// testable without a pty: anything that is not an unambiguous yes is a no, and a
// read error with no bytes is a no rather than a retry.
func parseConfirmation(line string, err error) bool {
	answer := strings.ToLower(strings.TrimSpace(line))
	if err != nil && answer == "" {
		return false
	}
	return answer == "y" || answer == "yes"
}

func countEncrypted(cfg *picoclaw.Config) int {
	n := 0
	for _, m := range cfg.ModelList {
		for _, k := range m.APIKeys {
			if credential.IsEncrypted(k) {
				n++
			}
		}
	}
	return n
}

// verifyOneDecrypts proves the passphrase and key actually open something in this
// workspace. Taking any single credential rather than all of them is deliberate:
// one failure is enough to say the route is broken, and decrypting all of them
// would hold every secret in memory at once for no extra signal.
func verifyOneDecrypts(passphrase, keyPath string, cfg *picoclaw.Config) error {
	for _, m := range cfg.ModelList {
		for _, k := range m.APIKeys {
			if !credential.IsEncrypted(k) {
				continue
			}
			if _, err := credential.Decrypt(passphrase, keyPath, k); err != nil {
				return fmt.Errorf("model %q: %w", m.ModelName, err)
			}
			return nil
		}
	}
	return nil
}

// within reports whether path sits inside root after cleaning both, so a key file
// that is not under the mounted picoclaw/ home is caught here rather than at
// gateway boot.
func within(root, path string) bool {
	relPath, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relPath != ".." && !strings.HasPrefix(relPath, ".."+string(filepath.Separator)) && !filepath.IsAbs(relPath)
}

// encryptedCredentialCount counts enc:// values across the whole config.json,
// using the document form because picoclaw.Config deliberately models only the
// fields Piccolo generates — channel_list is user-owned and absent from it.
// A count from the typed config alone would report "no credentials to protect" on
// a workspace whose channel tokens are all encrypted.
func encryptedCredentialCount(doc *configdoc.Doc) int {
	n := 0
	walkValue(doc, &n)
	return n
}

func walkValue(doc *configdoc.Doc, n *int) {
	for _, k := range doc.Keys() {
		v, ok := doc.Get(k)
		if !ok {
			continue
		}
		walkAny(v, n)
	}
}

func walkAny(v any, n *int) {
	switch t := v.(type) {
	case string:
		if credential.IsEncrypted(t) {
			*n++
		}
	case []configdoc.Value:
		for _, e := range t {
			walkAny(e, n)
		}
	case *configdoc.Doc:
		walkValue(t, n)
	}
}
