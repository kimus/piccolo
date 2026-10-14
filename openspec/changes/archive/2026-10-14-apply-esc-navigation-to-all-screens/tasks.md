## 1. One definition

- [x] 1.1 `wizard.KeyMap()` binds esc alongside ctrl+c
- [x] 1.2 `wizard.Form()` installs it; `setupForm` delegates rather than rebuilding
- [x] 1.3 All four integration form sites and the init wizard go through the constructor

## 2. Behaviour it unlocks

- [x] 2.1 esc inside an action form returns to the menu, nothing staged
- [x] 2.2 esc at the menu exits without an `Error:` line
- [x] 2.3 aborting a confirm gate means "no" (staging declined message, config discarded), not a failure
- [x] 2.4 a session that proposed nothing no longer claims a pending change was left behind

## 3. Advertising it

- [x] 3.1 `huh` renders no abort help of its own, so the hint lives in screen text: "esc quit" at the menu, "esc back to the menu" on the first field of a form
- [x] 3.2 hint appended to the existing keep/current-value sentence rather than replacing it

## 4. Checks

- [x] 4.1 keymap test: esc and ctrl+c abort, enter and tab do not
- [x] 4.2 `go build`, `go vet`, `go test ./...`
- [x] 4.3 pty run confirms both screens print the hint and behave (menu and form text captured)
- [ ] 4.4 `piccolo init`'s wizard screens confirmed by the shared constructor rather than a dedicated pty run
