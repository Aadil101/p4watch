# p4watch

A Go daemon that keeps a reconciled Perforce workspace-status answer warm and
serves it over a local endpoint in constant time. See `README.md` for the problem
and the architecture, and `CONTEXT.md` for the domain vocabulary — read them,
this file doesn't restate them. Decisions with their rationale live in two
places: the **README** is canonical for the architecture and the correctness
stance (events vs backstop, why an unknown answer is never reported as clean),
and `docs/adr/` holds decisions that have no natural home in it. Read whichever
touches what you're about to change, and say so explicitly if your work
contradicts one rather than silently overriding it.

**Don't restate the README in an ADR.** If a decision is already argued there,
that's the source of truth; an ADR repeating it more thinly is the copy that
will drift.

**Maintenance rule:** this file holds only what you can't learn by reading the
repo. Durable conventions and decisions-plus-rationale, a line or two each. Not a
status report — don't record what currently works or what's left to do.

## Build and test

```
go build ./...
go test ./...
go vet ./...
```

`p4watchd.exe` at the root is a local build artifact, not something to edit.

## Conventions

- **Tests use testify** — `require` for preconditions that must halt the test
  (`require.NoError` after setup), `assert` for the assertions under test. Never
  bare stdlib `t.Errorf` / `t.Fatalf`.
- **No em-dashes anywhere in this repo** — code, comments, docs, commit messages.
  Rephrase, or use a comma, parenthesis, or colon.
- **Comments must be self-contained.** Never cite the internal design/vision doc
  from a source comment; a reader with only this repo has to be able to follow
  it. Explain the reasoning inline instead of pointing at something they can't
  open.
- **Use `CONTEXT.md`'s vocabulary.** Say re-anchor, not reconcile, for what the
  daemon does; workspace or client name, never bare "client". If a concept isn't
  in the glossary yet, that's a signal: either it's language the project doesn't
  use, or the glossary has a gap worth filling.
- **Commit subjects: imperative and short for structural work** ("Extract daemon
  core into internal/daemon"). A commit that changes observable behaviour instead
  gets one sentence stating the new behaviour and why it matters to a caller. No
  multi-paragraph bodies either way. (Inferred from existing history — correct me
  if the long-subject commit was a one-off.)
- **Ask before adding a dependency.** The point of this project is that it is a
  small, legible systems daemon; the module graph staying near-empty is part of
  that.

## Positioning

- **v1 is a correctness project, not a speed project.** The claim that matters is
  being the first thing that is *both* correct and cheap to ask — the empty
  quadrant in the README's table. Lead with that, not with a speedup multiplier
  over `p4 reconcile`; the multiplier is a consequence, and quoting it invites an
  argument about benchmark conditions rather than about the idea.
- **Numbers here and in the README must agree**, and the README wins by default.
  Prefer the conservative figure unless a reproducible benchmark in the repo
  backs the sharper one.
