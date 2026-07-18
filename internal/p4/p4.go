// Package p4 is the deliberate boundary between p4watch and the proprietary
// `p4` binary. Shelling out to `p4` is acceptable (lazygit shells out to `git`),
// but the boundary must be explicit and small. Everything that knows about
// Perforce command syntax lives here; nothing else does.
package p4

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Client identifies a Perforce connection + workspace.
type Client struct {
	Port   string // P4PORT, e.g. "localhost:1666"
	User   string // P4USER
	Client string // P4CLIENT (workspace name)
	Root   string // local workspace root
	Bin    string // path to the p4 executable ("" -> resolve "p4" on PATH)
}

// DepotFile is what the SERVER says a file should be: its size and content
// digest at the head revision.  `p4 fstat -Ol` hands us these in ~0.5s for
// 100k files, so we never compute depot digests ourselves.
type DepotFile struct {
	Size   int64
	Digest string // uppercase MD5 hex, as Perforce reports it
}

func (c *Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "p4"
}

// args prepends the global connection flags so we never depend on ambient
// P4PORT/P4USER/P4CLIENT env: the daemon must be explicit about which server
// and workspace it speaks for.
func (c *Client) args(rest ...string) []string {
	a := []string{"-p", c.Port, "-u", c.User, "-c", c.Client}
	return append(a, rest...)
}

// DepotState returns the server's view of the whole workspace, keyed by local
// path in a case-normalized form (NTFS is case-insensitive).
//
// It uses tagged output (`-ztag`) rather than -G marshalled Python dicts. The
// -ztag text parser is what v1 ships; switching to -G is known future work
// (see the README's "Known gaps").
func (c *Client) DepotState(ctx context.Context) (map[string]DepotFile, error) {
	// -Ol  -> include fileSize + digest of the head revision.
	// -T   -> restrict to the fields we parse, so a schema we don't expect
	//         can't inject surprise lines.
	// //client/... -> the whole workspace, in client syntax.
	whole := fmt.Sprintf("//%s/...", c.Client)
	cmd := exec.CommandContext(ctx, c.bin(),
		c.args("-ztag", "fstat", "-Ol",
			"-T", "clientFile fileSize digest headAction",
			whole)...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("p4 fstat: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	out := make(map[string]DepotFile, 1<<14)
	var rec struct {
		clientFile string
		size       int64
		digest     string
		action     string
		haveSize   bool
	}
	flush := func() {
		// Skip anything the server can't give us a concrete size+digest for:
		// deletes (headAction delete), symlinks, and other non-comparable types.
		// A record we can't compare is not a clean/dirty signal; it's absence.
		if rec.clientFile != "" && rec.haveSize && rec.digest != "" &&
			!strings.Contains(rec.action, "delete") {
			out[normPath(rec.clientFile)] = DepotFile{Size: rec.size, Digest: rec.digest}
		}
		rec.clientFile, rec.size, rec.digest, rec.action, rec.haveSize = "", 0, "", "", false
	}

	sc := bufio.NewScanner(&stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // paths can be long
	for sc.Scan() {
		line := sc.Text()
		if line == "" { // blank line terminates a record in -ztag output
			flush()
			continue
		}
		key, val, ok := parseTag(line)
		if !ok {
			continue
		}
		switch key {
		case "clientFile":
			rec.clientFile = val
		case "fileSize":
			if n, err := strconv.ParseInt(val, 10, 64); err == nil {
				rec.size, rec.haveSize = n, true
			}
		case "digest":
			rec.digest = strings.ToUpper(val)
		case "headAction":
			rec.action = val
		}
	}
	flush() // last record may not be followed by a blank line
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("p4 fstat scan: %w", err)
	}
	return out, nil
}

// parseTag splits a `-ztag` line of the form "... key value" into (key, value).
// Perforce prefixes every tagged field with "... ".
func parseTag(line string) (key, val string, ok bool) {
	const prefix = "... "
	if !strings.HasPrefix(line, prefix) {
		return "", "", false
	}
	rest := line[len(prefix):]
	sp := strings.IndexByte(rest, ' ')
	if sp < 0 {
		return rest, "", true // key present, empty value
	}
	return rest[:sp], rest[sp+1:], true
}

// normPath case-folds a local path so lookups survive NTFS case-insensitivity.
// It does NOT alter separators; clientFile is already in OS-native form.
func normPath(p string) string {
	return strings.ToLower(p)
}
