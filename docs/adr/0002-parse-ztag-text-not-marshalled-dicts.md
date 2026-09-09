# ADR 0002: Parse `-ztag` text output, not `-G` marshalled dictionaries

**Status:** accepted for v1, with a known upgrade path

## Context

`internal/p4` reads the server's view of the workspace from `p4 fstat -Ol`. The
`p4` binary can emit that in two machine-readable forms:

- **`-ztag`**: line-oriented tagged text, one `... key value` line per field,
  records separated by blank lines.
- **`-G`**: a stream of Python `marshal`-format dictionaries.

## Decision

v1 parses `-ztag` with `bufio.Scanner` and string handling from the standard
library. Field selection is pinned with `-T clientFile fileSize digest
headAction`.

## Rationale

Go has no standard-library decoder for Python's `marshal` format, so `-G` means
either a third-party dependency or a hand-written binary parser. Neither is
justified while a stdlib text scan is sufficient, and keeping the module graph
near-empty is a deliberate property of this project.

`-T` narrows the parse surface so a schema field we did not anticipate cannot
introduce lines the parser mishandles, which removes most of the fragility a
text format would otherwise carry.

## Consequences

- **The record framing is newline-dependent.** A blank line terminates a record
  and `... ` prefixes a field, so a client path containing a newline would
  corrupt record boundaries. This is not reachable on the current target: NTFS
  forbids newlines in filenames, which is the same platform assumption
  `normPath` already encodes for case-insensitivity. It becomes a real
  correctness bug the day this runs on a filesystem that permits them, so
  **treat cross-platform support and the `-G` migration as one piece of work,
  not two.**
- Long paths need an enlarged scanner buffer (4 MB), already set.
- A record lacking a concrete size and digest (deletes, symlinks,
  non-comparable types) is dropped rather than guessed at: absence, not a
  clean/dirty signal.
- Switching to `-G` is confined to `internal/p4` by ADR 0001, so it stays a
  one-package change.
