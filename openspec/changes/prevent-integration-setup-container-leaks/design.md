## Context

Setup and status run integrations as short-lived attached containers. Three properties combined badly:

1. Names embedded `time.Now().UnixNano()`, so no future run could recognise a previous one's container.
2. Cleanup happened only on the success path (`proc.Wait` → `proc.Shutdown`); an interrupt or killed Piccolo left the container running.
3. A PTY was requested purely from the descriptor (`SetupRequiresTTY`). When the host had no terminal, `crossterm`/`inquire` still read `/dev/tty`, so closing the attach stream delivered no EOF and the container blocked indefinitely — verified by `docker top` showing a live `piccolo-gws setup` PID.

Because the descriptor also published a fixed host port (`127.0.0.1:8080:8080/tcp`), the first leak made every subsequent setup fail with `port is already allocated`.

## Goals / Non-Goals

**Goals:**
- No Piccolo one-shot can survive an interrupted run without being detected and reclaimed.
- Non-interactive invocation must terminate, not block.
- Leaks are visible in `doctor` before they surprise the user.

**Non-Goals:**
- No change to the OAuth port strategy. (Separate finding: `gws auth login` binds `127.0.0.1:0` — an ephemeral container port — so a published host port can never carry the callback. Removing `setup_ports` and adding host-side credential import is a separate change, because it alters login UX.)
- No support for two concurrent setup runs of the same integration.

## Decisions

- **Deterministic names over label-based GC.** `piccolo-setup-<integration>` makes reclaim a cheap prefix operation and mirrors `serve`'s existing stale-replacement-by-name behavior (acceptance criterion 9). A `docker label` + background GC sweep was rejected: labels already exist, but nothing would trigger cleanup at the right moment, and a swept-while-running hazard appears with unique names.
- **Role-separated prefixes** (`piccolo-setup-` vs `piccolo-setup-status-`) so a status run cannot kill a user's in-progress setup.
- **Reclaim before start, and report it.** Silent reclamation was rejected — a user needs to know their previous attempt was aborted.
- **Gate the PTY on host `term.IsTerminal`.** This is the actual leak source. When false, Piccolo runs the integration's documented non-interactive path, which exits promptly. Chosen over "kill the container when stdin closes", which cannot work: a PTY read never sees EOF from the attach stream.
- **`signal.NotifyContext(os.Interrupt, SIGTERM)` around the wait**, matching `chat.go` and `serve.go`, so Ctrl-C returns from `Wait` and `Shutdown` still runs. The TTY is also restored before follow-up output.
- **Not-found during inspect becomes a sentence, not a 404.** Deterministic names make concurrent Piccolo invocations able to reclaim each other's container; the message names the cause instead of leaking `No such container: <hex>`.

## Risks / Trade-offs

- [Reclaim can remove a container another live Piccolo is using] → accepted; setup is single-operator by design, and the failure is now an explicit message rather than a raw error.
- [A leaked container from an older Piccolo has a timestamped name] → still reclaimed: the sweep matches the `piccolo-setup-<integration>` prefix, which is a prefix of legacy names too. Verified live against `piccolo-setup-gws-1791932406936295000`.
- [Non-TTY users silently lose the wizard] → Piccolo prints `No terminal attached; running setup non-interactively.` so the behavior change is visible.
