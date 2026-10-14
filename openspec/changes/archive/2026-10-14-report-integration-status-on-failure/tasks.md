# Tasks: report-integration-status-on-failure

## 1. Capture exit code alongside output

- [x] 1.1 Add a capture helper that returns stdout, stderr, and the container exit code without erroring on non-zero exit
- [x] 1.2 Keep the existing `integrationRunner` interface intact so test fakes need no rework

## 2. Status path classifies by payload

- [x] 2.1 Try to parse captured stdout as a `piccolo.integration.status.v1` report before judging the exit code
- [x] 2.2 Render the report when parsing succeeds, marking the run unhealthy when the exit code is non-zero
- [x] 2.3 Preserve the invocation-failure error (exit code + stderr) when stdout is empty or unparseable
- [x] 2.4 Exit non-zero from `piccolo integration status` whenever checks failed or the command itself failed

## 3. JSON mode

- [x] 3.1 Emit the parsed payload for `--json` regardless of exit code
- [x] 3.2 Fail with the invocation error for `--json` when the payload is not parseable

## 4. Verification

- [x] 4.1 Test: non-zero exit with valid payload renders checks and remediation instead of an opaque error
- [x] 4.2 Test: non-zero exit with garbage stdout still reports an invocation failure
- [x] 4.3 Test: zero exit renders normally and exits zero
- [x] 4.4 Test: `--json` emits the payload on a failing run and still exits non-zero
- [x] 4.5 Run `go test ./...` and `openspec validate report-integration-status-on-failure --strict`
- [x] 4.6 Update README to state that failing checks are a rendered report, not a Piccolo error
