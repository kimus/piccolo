## Why

`piccolo integration setup kimus/piccolo-gws` rendered one field — `project_id`, an *optional* one — and treated Enter on it as a finished form. Two of the three fields the action declared were never shown: `client_id` and `client_secret` carry `whenUnmet: credentials`, and `credentials` was already true, so Piccolo deleted them from the form before rendering. The integration then received `{"project_id":"aaa"}` and correctly answered `missing_input`.

The suppression rule that caused it is not a corner case: it fires for any action whose inputs are gated on a fact the same action is the only way to set. "Set OAuth client credentials" becomes unfillable the moment credentials exist, so there is no path to rotate a client secret, and no path to re-enter credentials after a wrong one.

The design being adopted comes from that discussion: an action's inputs are **always rendered and always pre-filled with their stored values**, so a field left as-shown keeps its value and a field deliberately emptied clears it. Without pre-fill, "skip" and "clear" are the same two bytes and an optional field can never be unset.

## What Changes

- **Piccolo never omits a declared input.** `whenUnmet` stops suppressing fields; the descriptor may keep sending it to older hosts, and Piccolo now ignores it. This is the defect fix.
- **Inputs pre-fill from integration state.** Non-sensitive values arrive as stored; a sensitive value is *never* transmitted, and its field is presented as already-set through its description instead.
- **`required` inputs cannot be advanced past or submitted empty**, using per-field validation, so a half-filled form can never reach an integration.
- **Answers now carry blanks.** An emptied optional field is sent as an empty string meaning *clear*; a sensitive field the user did not touch is omitted meaning *keep*. This replaces today's rule that silently discards every empty value.
- **ESC returns to the menu** and saves nothing for that action, while a configuration patch proposed by an already-completed action is kept — today ESC aborts the whole setup and discards it.

## Capabilities

### New Capabilities

### Modified Capabilities

- `integration-setup-actions`: inputs are always rendered, pre-filled, and validated for requiredness; the answers document expresses keep versus clear; backing out is a form-level action rather than a fatal error

## Impact

- `internal/integration/actions.go` — `ActionInput` gains `Required`; add `State.Values` for pre-fill (unknown JSON keys are already ignored, so this is additive inside schema v1)
- `cmd/piccolo/integration_setup.go` — delete the filter in `inputsFor` (`:780`), pre-fill holders, per-field required validation, answer encoding (`:412` currently drops blanks), and `huh.ErrUserAborted` handling at `:679`
- Docs: the setup section describes pre-filled fields, Enter-keeps, delete-clears, ESC-keeps-earlier-work
- No protocol-version bump. `piccolo-gws 0.3.2` already in this workspace's image sends no `required` and no values, so pre-fill and requiredness land with the companion gws release; Piccolo's side alone still removes the truncated form.
