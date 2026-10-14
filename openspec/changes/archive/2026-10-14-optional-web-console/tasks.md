## 1. Reframe generation (web console on/off)

- [x] 1.1 In `internal/generate`, replace the `Mode` enum (`ModeTerminal`/`ModeWeb`) and `Inputs.Mode` field with a `Dashboard bool` on `Inputs`; update `Validate()` accordingly.
- [x] 1.2 Update `Inputs.Manifest()`: `WebConsole == false` → `DefaultImage`, no `:18800` binding; `WebConsole == true` → `LauncherImage` + `127.0.0.1:18800:18800` + `PICOCLAW_GATEWAY_HOST=0.0.0.0`. Add/adjust unit tests for both cases.
- [x] 1.3 Add a generation test asserting `tools.web.enabled` (web search) is independent of the console choice (web search on + console off yields no `:18800` binding).

## 2. Setup / wizard wording

- [x] 2.1 Replace the terminal/web Select in `internal/wizard` with a Confirm ("Expose the optional web console?"), worded so it is clear the gateway always runs; wire it to `Inputs.Dashboard`.
- [x] 2.2 In `cmd/piccolo/init.go`, replace the `--mode` flag with `--dashboard` (bool, default false); update `initFlags`, `Prefill`, `--yes` defaulting, and `inputsFromPrefill`.
- [x] 2.3 In `cmd/piccolo/init.go`, rename the `--web` flag to `--web-search` (Go field `web`/`WebSearch` and the wizard prompt stay); confirm no remaining bare `--web` flag.
- [x] 2.4 Update the `init` plan-preview output so the console choice reads as "web console: on/off" alongside the selected image.

## 3. Serve / status behavior

- [x] 3.1 Verify `cmd/piccolo/serve.go` prints the web URL + `/launcher-setup` hint only when `webConsoleURL(m) != ""`; adjust wording to frame it as the optional console.
- [x] 3.2 Confirm serve never publishes `:18800` when the binding is absent (only the auto-published gateway port); add/adjust a spec-composition unit test for the console-off case.
- [x] 3.3 Update `cmd/piccolo/lifecycle.go` `status` to print the console URL only when enabled.

## 4. Doctor consistency check

- [x] 4.1 Add a doctor check: a `:18800` binding present while `container.image` is not the launcher image → `[WARN]` (published port with no dashboard behind it).
- [x] 4.2 Ensure doctor raises no warning/failure attributable solely to a disabled console; add unit tests (daemon-free via the existing interface) for both the mismatch warning and the clean console-off case.

## 5. Docs

- [x] 5.1 Update `README.md` to describe the web console as an optional service (gateway always required), including how to enable/disable it by editing `piccolo.yaml`.
- [x] 5.2 Update `AGENTS.md` (Command Behavior / Decisions Log) to reflect the reframing: `:18800` binding is the single source of truth; `--mode` → `--dashboard`; `--web` → `--web-search`; console-off runs the lean gateway image with no dashboard process.

## 6. Verification

- [x] 6.1 `go build ./...` and `go test ./...` pass.
- [x] 6.2 Live smoke: console-off workspace → `piccolo serve` health-gates on `:18790`, nothing on `:18800`, `piccolo chat -m` works.
- [x] 6.3 Live smoke: console-on workspace → `piccolo serve` publishes `:18800`, `/launcher-setup` reachable, gateway healthy.
- [x] 6.4 `openspec validate --change optional-web-console` passes.
