# Tasks: prevent-integration-setup-container-leaks

## 1. Deterministic one-shot naming

- [x] 1.1 Add a shared `SetupContainerPrefix` constant in `internal/docker`
- [x] 1.2 Derive setup container names from the integration name (`piccolo-setup-<name>`), dropping the per-run nanosecond suffix
- [x] 1.3 Give status one-shots a distinct sub-prefix (`piccolo-setup-status-<name>`) so roles cannot reclaim each other
- [x] 1.4 Sanitize integration names into container-safe form

## 2. Reclaim abandoned containers

- [x] 2.1 Add `Runner.ListByPrefix` / `Runner.RemoveByPrefix` (stop + force-remove, Piccolo-prefixed names only)
- [x] 2.2 Sweep matching abandoned containers before starting a setup one-shot
- [x] 2.3 Report the number and names of reclaimed containers to the user
- [x] 2.4 Verify legacy timestamped orphans are matched by the prefix sweep

## 3. Interrupt-safe cleanup

- [x] 3.1 Wrap the setup container wait in `signal.NotifyContext(os.Interrupt, SIGTERM)` so `Shutdown` runs on Ctrl-C
- [x] 3.2 Apply the same signal-aware wait to the shared capture helper used by status and describe
- [x] 3.3 Restore the host terminal before printing follow-up output

## 4. PTY correctness

- [x] 4.1 Create the one-shot PTY only when the integration requires a TTY **and** host stdin is a terminal
- [x] 4.2 Tell the user when a TTY-requiring integration runs non-interactively
- [x] 4.3 Enter raw mode on the host for interactive runs and restore it on exit (including error paths)
- [x] 4.4 Add `docker.Resizable` + `containerProcess.Resize` and sync host window size into the container PTY on start and on resize

## 5. Diagnostics

- [x] 5.1 Turn a not-found container during the exit poll into a human-readable message naming the likely cause
- [x] 5.2 Add a `doctor` warning for abandoned Piccolo one-shot containers (warning only, never a failure)

## 6. Verification

- [x] 6.1 Test: reclaim only matches Piccolo-prefixed names and never targets the gateway
- [x] 6.2 Test: setup/status names are deterministic and non-overlapping
- [x] 6.3 Test: doctor warns on orphans without failing
- [x] 6.4 Run `go build ./...`, `go vet ./...`, `go test ./...`
- [x] 6.5 Live-verify: an existing orphan is reclaimed, a piped non-TTY setup run terminates with exit 0, and no containers remain afterwards
