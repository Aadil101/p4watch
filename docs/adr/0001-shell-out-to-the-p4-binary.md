# ADR 0001: Shell out to the `p4` binary, behind one package

**Status:** accepted

## Context

p4watch needs the server's view of the workspace. Perforce's wire protocol is
proprietary; the supported ways to get at it are the `p4` command-line binary or
the C++ API. Talking to Perforce from many places in the codebase would spread
knowledge of command syntax and output formats across the whole daemon.

## Decision

Shell out to the `p4` binary, and confine every piece of code that knows
Perforce command syntax to `internal/p4`. Nothing outside that package invokes
`p4` or parses its output.

Connection flags (`-p`, `-u`, `-c`) are always passed explicitly rather than
inherited from ambient `P4PORT` / `P4USER` / `P4CLIENT` environment variables:
the daemon is long-lived and must be unambiguous about which server and
workspace it speaks for.

## Rationale

Shelling out to the vendor binary is established practice for tools in this
shape (lazygit shells out to `git`), and it avoids a build dependency on the C++
API. The cost is that the boundary is textual and therefore fragile, which is
exactly why it is confined to one small package rather than allowed to spread.

## Consequences

- The `p4` binary must be on `PATH`, or its location supplied explicitly.
- Output-format changes in a future Perforce release break parsing in one
  package rather than across the daemon.
- Field selection is restricted (`-T`) so an unexpected schema cannot inject
  lines the parser does not expect.
- v1 parses tagged text output (`-ztag`). Switching to `-G` marshalled
  dictionaries is known future work and is confined to this same package.
