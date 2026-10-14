## Context

Tonight's form was correct as rendered and wrong as *designed*. The descriptor said:

```json
{ "key": "client_id",     "type": "text",   "whenUnmet": "credentials" },
{ "key": "client_secret", "type": "secret", "whenUnmet": "credentials", "sensitive": true },
{ "key": "project_id",    "type": "text" }
```

and with `credentials: true`, `inputsFor` returned one input. One `huh` group per input means one page, so Enter was submit, not next. The stored `client_secret.json` did contain both fields — the `credentials` fact was **true** — so this was not a bad fact and not a bad validation; it was a form that deleted its own subject fields and then treated the survivor as complete.

Two further facts constrain the fix:

```go
if h.text != "" { values[h.key] = h.text }   // :412 — blanks are unsendable today
```
```rust
/// Protocol emitter: stdout only, and never any secret value.   // gws actions.rs:184
```

The first is why "absent means keep" is not good enough: an optional field that holds a value can never be emptied. The second is why the same rule cannot be extended to secrets: making Piccolo pre-fill a secret requires the integration to print it on stdout, into scrollback, tmux pipes, `script(1)` captures and CI logs. The library supports everything needed already: `Input.Validate` (`field_input.go:232`), `Input.Description` (`:102`), `Input.EchoMode` (`:195`), `ErrUserAborted` (`form.go:51`).

## Goals / Non-Goals

**Goals:**
- Every declared input is always renderable, and a required one cannot be skipped.
- Skip and clear are distinguishable for every field where that is meaningful.
- A secret's value never travels from the container to the host.
- Backing out of a form is a normal user action, not a failed command.

**Non-Goals:**
- No requiredness enforcement in the integration *for the renderer's sake* — gws keeps validating, because a renderer is not a trust boundary.
- No two-phase save (`preview`/`commit` events). One submit per form is the transaction.
- No new descriptor or state schema version.
- No clearing mechanism for *optional and sensitive* fields. That combination is unrepresentable without sending the secret out or inventing a control nobody has asked for; the design leaves it out and says so.

## Decisions

### Inputs are never omitted

`whenUnmet` stops filtering. An action declares fields; the form shows all of them. This alone fixes the reported defect and it is the precondition for requiredness meaning anything: a field deleted before rendering cannot be validated.

Rejected: keeping the filter and adding a "replace" confirmation. That leaves the field invisible by default, so a user who does not think to replace still submits a partial record, and every integration inherits the same trap.

Rejected: making Piccolo refuse to call an action whose inputs were all suppressed. That converts a wrong form into a dead action — the credentials action would simply become unselectable once configured, which is the same bug wearing an error message.

### Pre-fill from state, and `required` blocks progress

`state` grows a `values` object mapping input key to the stored value for non-sensitive fields. Piccolo seeds each field's target so Enter-on-unchanged resubmits what is already stored — behaviourally identical to "keep", but it makes the user *see* the current value, which is the whole point. A required input gets `Validate(ValidateNotEmpty())`, so the user cannot advance or submit past it; deleting a required value is therefore impossible, and clearing is reserved for optional fields, which is coherent because an optional field is precisely the one where blank is a meaningful choice.

`whenUnmet` remains parseable so a new Piccolo can drive an old bundle that still sends it; it is simply ignored. Old Piccolo with a new bundle keeps filtering, which is why the gws release carries its own side of the fix (see sequencing).

### A secret is presented as a state, not a value

For a `secret`/`sensitive` input whose field is already set: render empty, with `Description` "already set — Enter to keep, type to replace". Untouched means the key is omitted and the integration keeps its stored value. Typed means replaced. There is deliberately **no** sentinel value in the field: the first draft of this design pre-filled a `••••••••` stand-in *as the input's value*, and that fails — clicking End and typing produces `••••••••x`, which is a wrong secret that validates as present. Putting the mask in the display rather than the value removes that class entirely.

### The answers document says keep or clear, and it must not be inferred

- key present, non-empty → set
- key present, empty → **clear** (only reachable on optional, non-sensitive fields)
- key absent → keep (only emitted for an untouched sensitive field)

Today all three collapse into "non-empty", which is why `project_id` alone could be submitted. Encoding it explicitly also means a user genuinely clearing a field cannot be misread as keeping it.

### ESC is a back-gesture with a boundary

`ErrUserAborted` from the form returns to the menu, starts no container, writes nothing. A `result.configPatch` from an action that already completed stays pending and is applied after the loop, because it belongs to the completed action and not to the form being abandoned — the current `return err` at `:679` throws it away.

## Risks / Trade-offs

- **`required` lies if an integration omits it.** Absent means not-required, so an integration that declares nothing keeps today's behaviour and can still be submitted empty. The fix is only as good as the descriptor; the companion gws change marks both credential fields required.
- **An untouched sensitive field relies on the integration honouring omission.** gws must treat "key absent" as keep, not as clear. That is a real behaviour change in its handler and it is the riskiest line in the companion change.
- **Optional-and-sensitive cannot be cleared.** Stated rather than hidden; if a future integration needs it, the honest control is an explicit toggle, not a blank.
- **Pre-filled values cross stdout.** Client id and project id are not secrets but they are identifying; Piccolo must not log values, and `--json`-style output paths must not print them either.
- **Two deploys, not one.** The renderer fix is useful immediately; the full flow needs a gws release plus the promote cycle this workspace now has.

## Migration Plan

None, by project policy. `piccolo integration add kimus/piccolo-gws@<new>` then a gated promotion adopts the integration side. Against an older bundle the new Piccolo still renders every declared field, which is already an improvement, but with no `required` metadata it cannot block an empty submit.

## Open Questions

- Should clearing credentials be an action at all (`reset_credentials`), rather than being impossible by construction? Required fields cannot be emptied, so today "remove my credentials" has no gesture.
- Should `Description` show a masked hint of what is stored (length, last characters) to help a user identify which client is configured, or does any partial echo of a secret-adjacent value cross a line we do not want to cross? (client_id is not secret, so this may only matter for display of the id itself.)
