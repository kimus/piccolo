## Why

`/status` in a live workspace produced `Tool execution failed error="Command blocked by
safety guard (path outside working dir)"` for a turn that only wanted uptime and uname.
PicoClaw's shell guard consumes the raw `restrict_to_workspace` flag, not the read access
relaxed by `allow_read_outside_workspace` (pkg/agent/instance.go:85-86,113), and inspects
every absolute-path token (pkg/tools/shell.go:1172,1278) against only /dev pseudo-devices
and `tools.allow_read_paths` — so any workspace that enabled shell execution refused
ordinary commands, and the error string carried no remedy.

## What Changes

- Generated `config.json` writes `"restrict_to_workspace": false` **explicitly** (the key
  is absent-means-true upstream, pkg/config/defaults.go:29; the field keeps no omitempty
  so the posture cannot silently revert). `allow_read_outside_workspace` stays true.
- `piccolo setup` → Tools gains "Workspace path guard" for a deliberate opt back in.
- Doctor WARNS while the guard is in effect, naming the exec asymmetry and the screen.
- No migration: config.json is user-owned, so the warning is the remedy path.

## Capabilities
### Modified Capabilities
- `setup-ux`
