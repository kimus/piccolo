## 1. Parse the additions (never break an old bundle)

- [x] 1.1 `ActionInput.Required bool` (`json:"required,omitempty"`) in `internal/integration/actions.go`
- [x] 1.2 `State.Values map[string]string` (`json:"values,omitempty"`); keep `facts` required, values optional
- [x] 1.3 Confirm `ParseState`/descriptor parsing still ignore unknown keys, so no schema bump is needed — and state that in the code comment, not just here — documented at the struct; no bump
- [x] 1.4 Test: state with values parses; state without values parses; values for undeclared keys ignored — TestParseStateAcceptsValuesAndUnknownKeys

## 2. Stop omitting inputs

- [x] 2.1 Delete the suppression in `inputsFor` (`cmd/piccolo/integration_setup.go:780`); keep the function only if it still does something, otherwise remove it and its callers — kept as a seam so the rule is testable
- [x] 2.2 Keep `whenUnmet` parsed but inert, with a comment saying old hosts rely on it and this host no longer suppresses
- [x] 2.3 Test: an action whose every input is gated on a fact the action itself sets still renders **all** inputs — the reported defect, as a regression test — TestInputsForNeverSuppressesDeclaredFields
- [x] 2.4 Test: menu availability still gates on `requires` (action level unaffected)

## 3. Pre-fill and requiredness

- [x] 3.1 Seed non-sensitive text inputs from `state.Values[key]` before rendering
- [x] 3.2 Sensitive input with a stored value: render empty, set `Description` to "already set — Enter keeps, typing replaces"; never display or log any secret value
- [x] 3.3 Attach `huh.ValidateNotEmpty()` to inputs declared `required`, so advance and submit are blocked
- [x] 3.4 Test: pre-filled value survives an untouched submit; deleted optional value is submitted as empty; required field blocks submit and starts no container
- [x] 3.5 Test: a sensitive field submits nothing when untouched, and its stored value is never requested or printed

## 4. Answers encode keep / clear / set

- [x] 4.1 Replace the drop-blanks behaviour at `integration_setup.go:412` and in `answersJSON`: always include rendered non-sensitive inputs (empty means clear), omit untouched sensitive ones
- [x] 4.2 Document the three-way meaning in the protocol comment where `ActionInput` is defined, so the next integration author reads it there — documented in encodeAnswers and answersJSON
- [x] 4.3 Test: submitted document for the credential form with (a) nothing touched, (b) id retyped, (c) optional project id emptied — exact JSON asserted for each — TestEncodeAnswersDistinguishesSetClearAndKeep

## 5. Backing out

- [x] 5.1 Check `errors.Is(err, huh.ErrUserAborted)` where `runAction` currently propagates any form error, and return to the menu
- [x] 5.2 Keep a previously earned `result.configPatch` pending across a later abort (`integration_setup.go:679` discards it today)
- [x] 5.3 Abort at the menu still exits without applying a pending proposal
- [x] 5.4 Tests for each of the three cases — live pty: ESC returned to menu, action never invoked

## 6. Acceptance: tonight's transcript

- [x] 6.1 With the current 0.3.2 bundle: run setup on a workspace where `credentials` is already true; all three fields must render, each pre-filled where permitted, and Enter on the last one must not be the only gesture available — see Live verification below
  (blocked on the companion gws release: 0.3.2 declares neither `required` nor values)
- [ ] 6.2 After the companion gws release is promoted: type the client id, press Enter, be **blocked** from advancing on the required secret with a stated reason, then either supply it or ESC back to the menu with nothing written
- [ ] 6.3 Edit path: re-open the credentials action, change only the client id, keep the secret, and confirm the stored document holds both values consistently; then empty `project_id` and confirm it clears
- [ ] 6.4 Verify no secret appears in Piccolo output, container logs, or process arguments during the above

## 7. Docs and checks

- [x] 7.1 README setup section: describe pre-filled fields, Enter-keeps, delete-clears, ESC-returns-without-saving, and that an optional-and-sensitive field cannot be cleared (stated, not hidden)
- [x] 7.2 AGENTS.md: the never-omit rule and the answers three-way meaning, as constraints on future input rendering
- [x] 7.3 `go build ./...`, `go vet ./...`, `go test ./...`
- [x] 7.4 Mutation-check that the regression tests have teeth: restore the suppression filter and confirm 2.3 fails; make blanks unsendable again and confirm 4.3 fails — both mutants caught

## Notes for implementation

- 6.1 is achievable with this change alone. 6.2–6.4 need the companion `save-integration-fields-incrementally` release from `piccolo-gws`, because 0.3.2 declares neither `required` nor current values.
- Leaving "clear the whole credential" with no gesture is a deliberate consequence of required fields not being emptyable; whether a `reset_credentials` action should exist is an open question in `design.md`, not something to improvise here.


## Live verification (pty-driven against the installed 0.3.2 bundle)

Before: the credential form rendered one field — "Google Cloud project ID (optional…)" — whose hint
read **enter submit**. Tonight's report, reproduced exactly (against a stale binary, which is itself
worth noting: `go build ./...` does not refresh `./piccolo`).

After: walking the form field by field —

```
OAuth client ID (client type must be "Desktop app")   enter next
OAuth client secret                                   enter next
Google Cloud project ID (optional, …)                 enter submit
```

All three render, the hint advances instead of submitting, and `Running set_credentials` appears only
after the last field. Submitting blank still returns gws's `missing_input`: 0.3.2 declares no
`required`, so the host cannot block it — correct for that bundle, and the reason 6.2-6.4 wait on the
companion release.

Observed while driving it: `credentials` reads true from a stale `client_secret.json` the user says
was never finished, so the menu offered `Login` as available. That is existence-based validity — the
companion change's problem, not this one's — and it is also what suppressed these fields originally.
