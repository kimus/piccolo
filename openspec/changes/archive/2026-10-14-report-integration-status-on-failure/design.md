## Context

`runIntegrationStatus` captures container stdout/stderr through `runContainerCapture`, which returns an error whenever the process exits non-zero. Integrations report unhealthy state by exiting non-zero with a valid status document on stdout (the contract `piccolo-gws` already implements). The report is discarded; the user sees only an exit code.

## Goals / Non-Goals

**Goals:**
- A non-zero exit with a parseable status payload renders the full report.
- Exit code and overall report status stay visible, so "checks failed" is not silently green.
- Preserve error behavior when stdout is not a status document.

**Non-Goals:**
- No change to the status payload schema or to how integrations decide pass/fail.
- No change to setup/describe paths, which have no report contract.

## Decisions

- **Classify by payload, not exit code**: capture raw stdout/stderr plus the exit code, then try `ParseStatus(stdout)`. Valid payload → render and mark the run unhealthy from the exit code. Invalid payload → surface the existing invocation error including stderr and the code. Alternative "trust exit code only" reproduces today's bug; alternative "always succeed" hides real container failures.
- **`--json` prints the payload unchanged**, so scripts can pipe it regardless of exit code, while the command's own exit status still reflects failure for CI use.
- **Keep `integrationRunner` interface unchanged** by adding a small capture helper that returns `(stdout, stderr, code)` instead of only an error — no test double churn beyond the existing fake.

## Risks / Trade-offs

- [A crashing integration could print a stale/valid-looking payload] → still rendered as a report, but Piccolo appends the non-zero exit note so it never reads as healthy.
- [Behavior change for scripts parsing Piccolo's stderr] → the exit code stays non-zero; only the message content improves.
