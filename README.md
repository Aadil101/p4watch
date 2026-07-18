# p4watch - fsmonitor for Perforce

A background daemon that answers **"what in my workspace differs from the server?"** for a Perforce client, and serves the answer over a local endpoint in constant time.

Think `git status`, but for Perforce, where the equivalent question is genuinely expensive.

## Why this exists

Perforce is server-authoritative and marks files read-only on disk until `p4 edit`. Any change made *outside* that flow (by a script, a build step, or an AI coding agent writing straight to a file) is invisible to Perforce until a `reconcile`, which stats the entire workspace and compares it against server state. On a large workspace (game and hardware repos run to millions of files) that is slow, because it is both disk-bound and server-bound.

That leaves two options, and both are bad:

| approach | cost on a 100k-file workspace | correctness |
|---|---|---|
| `p4 opened` | ~16 ms | **blind**, only knows what you told the server about via `p4 edit` |
| `p4 reconcile` | ~12 s | correct, but far too slow to call often |

Our daemon is built to be the first thing that is **both correct and cheap to ask**: it keeps a reconciled answer warm and serves it in well under a millisecond, so the question can be asked continuously (from a shell prompt, an editor, or an agent loop after every action) instead of a few times a day.

## How it works

```
                   file events (fsnotify)
                          │  debounced trigger
                          ▼
                  ┌───────────────────────────────────────────┐
   p4 fstat -Ol   │  reconcile pass                           │
  ───────────────▶  depot {size, digest}  ⨯  workspace walk  │
   (server hands  │  → dirty set (size check first,           │
    us digests)   │     hash only files whose size matches)   │
                  │  also runs on a periodic backstop timer   │
                  └──────────────────┬────────────────────────┘
                                     │ cache
                                     ▼
                     ┌────────────────────────────────┐
   shell prompt ───▶│  /summary  → dirty count, O(1)  │
   review UI ──────▶│  /status   → the dirty paths    │
                     └────────────────────────────────┘
```

The comparison is cheap because the server reports a size and content digest for every file (`p4 fstat -Ol`), so most files are settled by a `stat` alone (only files whose size still matches the server have to be read and hashed).

**Correctness comes first, speed second.** The daemon caches the result of a full reconcile pass and never reports a confident "clean" unless a pass has actually succeeded: until the first pass completes, or after one fails, its answer is *unknown* (`known: false`), which a caller must render differently from clean. A cheap wrong "all clear" is the exact failure this tool exists to avoid.

**Freshness comes from two sources, and only one of them is load-bearing.** A filesystem watch (fsnotify) triggers a debounced re-anchor, so the answer tracks edits within a fraction of a second instead of waiting for a poll. But the events are only a *trigger*: the re-anchor is a full reconcile, and a periodic *backstop* pass runs regardless. So a change that produced no event, or an event the OS dropped under load, can never keep the answer wrong for longer than the backstop interval (a dropped-events signal from the OS forces an immediate re-anchor on top of that). Events shrink the staleness window; the backstop, not the events, is the correctness guarantee. This is also what lets the daemon do no work at all when the workspace is idle, instead of re-scanning on a fixed timer.

## Status

Working today:

- Event-driven daemon (`cmd/p4watchd`) with `/summary`, `/status`, `/healthz`.
- Filesystem watch (fsnotify) triggers a debounced re-anchor; a periodic backstop bounds staleness if an event is missed, and a dropped-events signal forces an immediate re-anchor.
- Depot comparison via `p4 fstat -Ol`, size-first with hash-on-match.
- Honest failure states (`known` / `stale`) instead of false "clean".

Verified against a 100k-file local Perforce server: `/summary` responds in ~0.7 ms from cache, and the reported dirty set matches `p4 reconcile -n` path-for-path, including changes made behind Perforce's back that `p4 opened` does not see. With the backstop set to 5 minutes, creating and deleting a file each updated the answer within a few seconds off the filesystem event alone, proving the update came from the event path rather than the poll.

## Running it

```
go build -o p4watchd ./cmd/p4watchd

./p4watchd \
  -root     C:\path\to\workspace \
  -p4port   localhost:1666 \
  -p4user   you \
  -p4client your_client \
  -backstop 2m \
  -debounce 500ms

curl -s localhost:7778/summary
# {"known":true,"count":10,"age_ms":4120,"depot":100005,...}
```

`-backstop` is the periodic safety-net re-anchor interval, and `-debounce` is how long the watcher waits for edits to settle before re-anchoring. Between these, filesystem events drive normal refreshes and the backstop only has to cover changes the event stream misses.

## Known gaps (v1 scope)

- **Re-anchor efficiency.** Events now spare the daemon from re-scanning an idle workspace, but each re-anchor is still a *full* reconcile that re-hashes every same-size file, so a triggered pass is no faster than a one-shot reconcile tool. Hashing only the files an event actually touched (an incremental pass, with the full pass kept as the backstop) is the remaining optimization. The *served* answer is already O(1); this is the cost of the background pass only.
- **Text line-ending normalization.** Perforce stores the digest of the server-normalized form of text files, so a text file with foreign line endings can hash differently here even when Perforce would call it unchanged. Size-based detection is unaffected.
- **Tagged (`-ztag`) output parsing**, not the marshalled `-G` format (a more robust parser is a planned future work).
- **`.p4ignore`, view maps, and filetype special cases** are not yet applied.
- **Scope:** Windows, single workspace, **read-only** (status only; no `edit`, `revert`, or `submit`). A wrong read-only tool is annoying; a wrong write tool destroys uncommitted work.