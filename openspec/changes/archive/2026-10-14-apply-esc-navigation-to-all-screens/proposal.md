## Why

The baseline already requires it: `setup-ux` has "Setup uses ESC navigation" — implement *and advertise* esc as the local navigation key. It was written about `piccolo setup`, whose screens bind esc and print "esc quit"/"esc back". Integration setup never went through that constructor, so it kept the library default of ctrl+c alone: pressing esc while typing a credential did nothing, and the same key meant something else one screen earlier. A requirement scoped to one command silently licensed drift in every other screen.

## What Changes

- `wizard.KeyMap`/`wizard.Form` become the single constructor for interactive Piccolo screens, so the binding cannot diverge again.
- Integration setup forms and the action menu are built through it: esc leaves a form (back to the menu) and esc at the menu exits.
- Aborting a yes/no gate is a **no**, not an error. The escape key must not read as a crash on the screen whose job is to let the user decline.
- The hint is advertised in field/menu descriptions, the way `piccolo setup` does it — `huh` never renders its own abort binding, so a working key would otherwise stay invisible.

## Capabilities

### Modified Capabilities

- `setup-ux`: the esc requirement applies to every interactive screen, not just `piccolo setup`

## Impact

- `internal/wizard/wizard.go` (KeyMap, Form), `cmd/piccolo/setup.go` (delegates), `cmd/piccolo/integration_setup.go` (four form sites, two confirm gates, hint text)
- `internal/wizard/keymap_test.go` — esc and ctrl+c abort; enter and tab must **not**
- Safe by construction: `huh` evaluates the form-level abort before `group.Update`, and the file picker's own esc `Close` binding ships disabled, so no field loses a key it needs
