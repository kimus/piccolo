## Why

`piccolo integration typo`, `piccolo image typo`, `piccolo runtime typo` and `piccolo workspace typo` all exited **0** while printing group help and doing nothing. Cobra does this for a parent command with no runnable body, and it does it before any argument validation, so a hook cannot intercept it.

That is the worst shape a CLI mistake can take: a script or a person with muscle memory gets a green result from a command that never ran. It also made a retired command indistinguishable from a typo — `piccolo integration build` was removed with the Devbox/Nix toolchain, and after removal it printed help and exited 0, exactly as if it had succeeded.

Found while verifying the 0.3.0 release binary.

## What Changes

- Command groups (`integration`, `image`, `runtime`, `workspace`) become explicitly runnable: a bare group still prints help and succeeds, an argument that selects no subcommand **fails with a named error**.
- A near miss suggests the likely verb (`piccolo integration lst` → "did you mean 'piccolo integration list'?"). Suggestions consider retired commands, so a typo of a removed verb lands the user on the removal message instead of on silence.
- `piccolo integration build` remains present but hidden, and refuses with the reason and the `piccolo integration add` remedy, non-zero.

## Capabilities

### New Capabilities

- `cli-command-surface`: group commands report mistakes as mistakes

### Modified Capabilities

_None._

## Impact

- `cmd/piccolo/root.go` (shared guard + suggestion), `image.go`, `integration.go`, `runtime.go`, `workspace.go`
- `cmd/piccolo/cli_test.go` — exit-code and suggestion behaviour pinned
- Any script that typed a wrong subcommand now fails instead of passing. That is the point.
