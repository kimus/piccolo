package wizard

import (
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// The point of a shared keymap is that esc leaves every Piccolo screen, including
// the ones that shipped before anyone thought about it. If the library default
// ever wins again -- or if the fix leaks onto a key a field still needs -- this
// is what fails.
func TestKeyMapAbortsOnEscapeAndCtrlC(t *testing.T) {
	km := KeyMap()
	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyEscape},
		{Type: tea.KeyCtrlC},
	} {
		if !key.Matches(msg, km.Quit) {
			t.Errorf("%s does not abort a form", msg)
		}
	}
	// Enter must stay submit: binding it as an abort would make every field
	// unwinnable, which is worse than the bug being fixed.
	if key.Matches(tea.KeyMsg{Type: tea.KeyEnter}, km.Quit) {
		t.Error("enter was bound as an abort key")
	}
	// Tab navigation must survive, since that is how the user moves between fields.
	if key.Matches(tea.KeyMsg{Type: tea.KeyTab}, km.Quit) {
		t.Error("tab was bound as an abort key")
	}
}
