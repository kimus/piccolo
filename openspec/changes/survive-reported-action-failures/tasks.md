## 1. Classify instead of throwing

- [x] 1.1 `classifyAction` ranks terminal event above exit code, and a cancelled context above both
- [x] 1.2 `runIntegrationAction` returns an outcome; it no longer manufactures an error for a report
- [x] 1.3 Table test over the seven cases, including `error` event with exit 0 and cancel beating a report

## 2. Survive it

- [x] 2.1 Menu loop continues on reported failure, crash, cancel and timeout
- [x] 2.2 State re-read each pass already; confirm a crash path still redraws fresh facts
- [x] 2.3 `pendingPatch` kept across failure and abandonment; dropped only on leaving the menu
- [x] 2.4 Crash counted per action: a second consecutive death says stop retrying

## 3. Report it

- [x] 3.1 Closing summary lists unfinished actions only; filter inside `summarize`, not in the caller
- [x] 3.2 Command exits 0 after reported failures; Piccolo-side faults still abort
- [x] 3.3 `styler`: SGR chosen over a theming library so the enable flag is honest and testable
- [x] 3.4 Statuses coloured from `ev.Status`; text markers always retained
- [x] 3.5 `integration status` prints `remediationLabel`/`remediationUrl` it had been parsing and dropping

## 4. Live acceptance

- [x] 4.1 Against a genuinely failing action (Drive not enabled): menu returned, failure and link rendered, no `Error:` in output
- [x] 4.2 Re-drawn menu showed the descriptor description and the stored client, confirming state was re-read
- [ ] 4.3 Confirm the summary line interactively (the driver's terminal cancel at the menu did not register; the filter is covered by unit test instead)

## 5. Checks

- [x] 5.1 `go build ./...`, `go vet ./...`, `go test ./...`
- [x] 5.2 `go build -o piccolo` after every change — a verification run against a stale binary already cost hours this session
