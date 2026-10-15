## 1. Guard

- [x] 1.1 Shared `rejectUnknownArgs` sets `RunE` (bare group prints help; unmatched argument errors), `Args = ArbitraryArgs`, `SilenceUsage`
- [x] 1.2 Applied to `integration`, `image`, `runtime`, `workspace`
- [x] 1.3 First attempt via an `Args` hook verified as a **no-op** and replaced — cobra returns help for non-runnable parents before argument validation

## 2. Suggestions

- [x] 2.1 Edit distance ≤ 2 over subcommand names, minimum argument length 3
- [x] 2.2 Hidden/retired verbs included as candidates so a typo reaches the removal message
- [x] 2.3 Verified: `integration lst` → list, `image lst` → list, `runtime sttus` → status, `integration bulid` → build

## 3. Retired verb

- [x] 3.1 `integration build` kept hidden, refuses with reason + `piccolo integration add` remedy, exit non-zero
- [x] 3.2 Not listed in `integration --help`

## 4. Tests

- [x] 4.1 Every group rejects an unknown subcommand
- [x] 4.2 Bare group prints help and succeeds
- [x] 4.3 Retired verb refuses and stays unadvertised
- [x] 4.4 `go build`, `go vet`, `go test ./...` green
