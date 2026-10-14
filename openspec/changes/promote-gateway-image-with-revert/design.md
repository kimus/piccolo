## Context

Today's restart is destructive before it is known-good:

```go
// cmd/piccolo/restart.go
found, err := runner.StopByName(stopCtx, name, stopGrace)   // current gateway gone
...
return serve(cmd)                                            // gate runs afterwards
```

and `serve`'s gate is the unauthenticated `/health` probe. That probe cannot detect the failure mode we actually hit: an image whose binaries are unexecutable still answers `200 OK`, because the gateway itself is fine and only the Nix-backed helpers are shadowed.

Measured behaviour that this design depends on:

```text
stop old                     → published port released (listeners 0)
start differently-named      → OK, 1 listener on the same host port
rename running container     → OK: port bindings preserved, exec by new name works, no restart
stopped container keeps name → canonical name is taken until renamed or removed
```

Two consequences shape the sequence. A stopped container still owns its name, so the replacement cannot take the canonical name until the old one is renamed or removed — and `Start` removes any container holding the configured name:

```go
// internal/docker: "A stale container carrying the configured name must not block restart."
removeStaleByName(ctx, cli, prepared.Name)
```

so anything that keeps a revert target must not be sitting on that name. And renaming is safe at any time, including while running.

## Goals / Non-Goals

**Goals:**
- A restart that fails leaves the workspace running what ran before, with the reason printed.
- Readiness means the thing the assistant will use is executable, not merely that a port answers.
- A single, unambiguous rollback target: the previously running image.
- An interrupted promote is detectable and self-cleaning.

**Non-Goals:**
- No image pruning or N-deep history.
- No automatic rebuild of a stale image (see `bake-runtime-toolchain-into-image`: drift is reported, the user rebuilds).
- No zero-downtime requirement; a few seconds of unavailability during promote is accepted.

## Decisions

### Sequence, with the old container never renamed

```text
1. resolve target image
2. stop  <name>                 → releases published ports; keep the container as the revert target
3. rename <name> → <name>.prev  → frees the canonical name without destroying anything
4. start <name> from target image, labelled piccolo.role=candidate
5. gate:  /health  AND  docker exec <name> <integration binary> <probe> per installed integration
6a. pass  → remove <name>.prev; strip the candidate label
6b. fail  → capture diagnostics, stop+remove the failed container,
            rename <name>.prev → <name>, start it, report non-zero
```

The alternative — start the new container under a temporary name while the old one is stopped, then rename — was considered and is equivalent at the daemon level, and the first version of this document proposed it. The `.prev` form is chosen because it keeps one invariant that matters for the crash path: **the canonical name always belongs to the container that should be running.** After a crash, "nothing is running on `<name>`" is the only abnormal state to detect, instead of two candidate names to reason about.

### Gate executes, and reports what it ran

`/health` alone is not a readiness signal for this image, and PATH presence is worse — measured: `command -v piccolo-gws` succeeded in a container where `piccolo-gws state` printed `not found`. So the gate runs each installed integration's binary via `docker exec` and requires a parseable result, and the failure report quotes the probe it ran plus its output.

### Diagnostics are collected before the evidence is deleted

A failed container is deleted on revert, but only after its gate results, health body and last log lines have been captured into the report — this is the only record of *why* the promote failed, and `serve` otherwise keeps failed containers around precisely because that history matters.

### Revert restores the stopped container, not a rebuilt spec

`docker start` on the kept container restores the exact configuration that was known good: image, mounts, env, published ports. Recreating from the manifest would rebuild from *current* settings, which may have changed since the image was built — that is a different thing from "go back to what was working".

### One rollback slot, from labels

On a completed promote, record the image the workspace was previously running. `piccolo integration rollback` resolves that image, verifies it still exists locally, and drives the same promote sequence with it as the target — so rollback inherits the gate and the revert path instead of being a second, weaker code path. Image content labels from the sibling change let the message say what is being rolled back **to**:

```text
rolling back to 7c1d2e9f (gws 0.3.1, base sipeed/picoclaw:launcher)
```

### Crash recovery is one check

If a promote is interrupted, the next `serve` looks for `<name>.prev`: if it exists and nothing runs on `<name>`, it is restored and the incident reported. A stray candidate is always renamed, never orphaned under the canonical name, so this check has no ambiguity.

## Risks / Trade-offs

- **A few seconds of unavailability** between stop and healthy. Accepted; the alternative is a parallel candidate with its own port and name collisions.
- **Both containers write the same `/root/.picoclaw`** only in the window where the candidate is up and the old one is stopped, not running — so no double writer.
- **Kept `.prev` containers pin their image layers.** Disk grows by one container per promote, not by an image copy; the removal on success bounds it at one.
- **Revert can itself fail** — if the user pruned the image, if the ports are now stolen by another process, or if the old failure was environmental. Then Piccolo must say plainly that both the new and the previous image are unavailable and name what to run next. A "rolled back" line that is not true would be worse than the outage.
- **A healthy-but-wrong image passes the gate.** Automatic revert cannot catch that, which is exactly what the explicit rollback command is for.
- **Labels are metadata, not proof**; the gate never trusts them to decide pass/fail.
- **Reconciling with the existing "keep the failed container" behaviour:** this path deletes the failed candidate. It does so only after capturing diagnostics, and the running gateway is restored, so the container is no longer the only artefact worth keeping.

## Migration Plan

None, by project policy. This change only alters how `restart`/`serve`/`rollback` behave; the single new manifest field is optional and defaults to "no previous image", which is exactly the state of every existing workspace.

## Open Questions

- Should the promote gate also verify that reported integration versions match the image labels — cheap drift proof, but it makes the gate depend on metadata quality rather than behaviour?
- Should `piccolo integration rollback` accept an explicit version by resolving it to a locally present labelled image (rebuilding if absent), or stay strictly a previous-image switch for now?
