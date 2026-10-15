## Context

Cobra's `execute()` returns `flag.ErrHelp` for a command that is not runnable, before `ValidateArgs` runs. `ExecuteC` treats that as success. So the usual fix — an `Args` validator — was a silent no-op here; it was written first, tested, and did nothing. The group has to be runnable for its own logic to run at all.

## Decisions

### Make the group runnable and decide inside it

A shared guard sets `RunE` on each group: no arguments → print help, exit 0; unmatched argument → error. `SilenceUsage` keeps the error readable. Hiding behind `Args` or `PersistentPreRun` is not possible for the reason above, and the guard is applied by every group so a new group cannot forget the rule by accident.

### Suggestions use edit distance, including hidden verbs

Prefix matching alone misses the case that matters most (`bulid` shares two characters with `build`). Suggestions accept distance ≤ 2 over names of length ≥ 3, and consider hidden (retired) commands: the listing stays clean, but a typo of a removed verb points at the command that explains the removal.

### Retirement keeps a hidden stub instead of deleting the verb

Deleting the subcommand entirely is what produced the exit-0-help behaviour in the first place. A hidden stub is not a compatibility shim — it does no work and always fails — but it converts a silent success into a stated reason plus the replacement command.

## Risks / Trade-offs

- **Scripts that ignored a typo'd group command now fail.** Intended; a build that never ran should not be green.
- **Suggestions can be wrong.** They are advisory text on an error path that already refuses, and only fire within edit distance 2.
