# p4watch domain language

The vocabulary this project uses. Perforce overloads several of these, and two
are coined here, so prefer the definition below over the ambient Perforce
meaning. Use these terms in code, comments, commits, and issues.

## Coined here

- **re-anchor** — a full reconcile pass triggered by filesystem activity, as
  opposed to one run by the backstop timer. It is *not* `p4 reconcile`, and it
  is not incremental: a re-anchor re-walks the workspace and re-compares against
  depot state. Say "re-anchor" for the operation, never "reconcile", so the
  daemon's work is never confused with the Perforce command.
- **backstop** — the periodic pass that runs regardless of events. It is the
  correctness guarantee: it bounds how long a missed event can keep the answer
  wrong. Events shrink the staleness window; the backstop is what makes the
  window finite.
- **the answer** — the cached reconcile result the daemon serves. Callers ask
  for the answer; they never trigger a pass.

## Overloaded by Perforce, pinned here

- **client** — Perforce uses this for the connection, the workspace name
  (`P4CLIENT`), and the workspace itself. In this codebase, `Client` is the
  connection struct; the workspace name is the `Client.Client` field, and the
  on-disk location is `Client.Root`. In prose, say **workspace** for the files on
  disk and **client name** for the `P4CLIENT` string. Never bare "client" in
  prose.
- **reconcile** — bare, this means the Perforce command `p4 reconcile`. For what
  the daemon does, say **reconcile pass** or **re-anchor**.
- **depot state** — the server's view of what every file should be: size plus
  content digest at the head revision, from `p4 fstat -Ol`. Not "server state",
  not "remote".

## Core terms

- **dirty set** — the files that differ from depot state. The daemon's output.
- **settled** — a file resolved as unchanged without hashing it, because its size
  already differs (or matches under the size-first comparison). Most files settle
  on a `stat` alone.
- **known** — whether a reconcile pass has ever succeeded. `known: false` means
  *unknown*, and a caller MUST render it differently from clean. A cheap wrong
  "all clear" is the failure this tool exists to prevent, so never describe an
  unknown answer as clean, empty, or zero.
- **stale** — the answer is from a completed pass, but that pass is older than
  the freshness the caller asked for. Distinct from unknown.
- **debounce** — how long the watcher waits for edits to settle before
  re-anchoring.

## Vocabulary to avoid

- "sync" — means something specific and different in Perforce (`p4 sync` pulls
  from the server). This daemon never syncs.
- "scan" / "index" — vague about whether the server was consulted. Say
  **reconcile pass** or **workspace walk**.
