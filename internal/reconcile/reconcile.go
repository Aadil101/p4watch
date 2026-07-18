// Package reconcile computes the set of workspace files that differ from what
// Perforce has on the server. It is the correctness core of p4watch: given the
// server's view (from package p4) and the files on disk, it decides what is
// "dirty": changed, added, or deleted relative to the depot.
//
// The comparison is cheap by design. The server hands us a size and digest per
// file, so most files are settled by a stat alone: if the on-disk size differs
// from the depot size, the file is dirty and no hashing is needed. Only files
// whose size still matches are hashed, and only those. That keeps a full pass
// dominated by stat() rather than I/O.
package reconcile

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Aadil101/p4watch/internal/p4"
)

// Kind classifies why a path is dirty. Kept coarse on purpose; p4watch reports
// status, it does not stage changelists.
type Kind uint8

const (
	Modified Kind = iota // exists on disk and in depot, content differs
	Added                // exists on disk, not in depot (or depot has it deleted)
	Deleted              // in depot, missing on disk
)

func (k Kind) String() string {
	switch k {
	case Added:
		return "add"
	case Deleted:
		return "delete"
	default:
		return "edit"
	}
}

// Change is one dirty path. Path is the on-disk path in native form.
type Change struct {
	Path string
	Kind Kind
}

// Result is a full reconcile pass: the dirty set plus the numbers that let the
// daemon report honestly about how the answer was produced.
type Result struct {
	Changes    []Change
	DepotFiles int           // files the server knows about (comparable)
	Walked     int           // files seen on disk
	Hashed     int           // files we had to read to settle (size matched)
	Took       time.Duration // wall time of the pass
	At         time.Time     // when the pass completed
}

// Count is the O(1) question a shell prompt asks: how many files are dirty.
func (r Result) Count() int { return len(r.Changes) }

// Run performs one full reconcile: pull depot state, walk the tree, diff.
//
// This is deliberately the whole-workspace pass, the same shape as an existing
// one-shot reconcile tool. The daemon caches its output and re-runs it on a
// timer; making it incremental via filesystem events is a later step. Building
// the correct pass first means every intermediate state is a working tool.
func Run(ctx context.Context, c *p4.Client) (Result, error) {
	start := time.Now()

	depot, err := c.DepotState(ctx)
	if err != nil {
		return Result{}, err
	}

	res := Result{DepotFiles: len(depot)}
	seen := make(map[string]struct{}, len(depot))

	walkErr := filepath.WalkDir(c.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip it, don't abort the whole pass
		}
		if d.IsDir() {
			// Perforce metadata dirs are not workspace content.
			if strings.HasPrefix(d.Name(), ".p4") {
				return filepath.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		res.Walked++

		key := strings.ToLower(path)
		seen[key] = struct{}{}

		want, known := depot[key]
		if !known {
			// On disk but the server has no comparable revision for it: added.
			res.Changes = append(res.Changes, Change{Path: path, Kind: Added})
			return nil
		}

		info, statErr := d.Info()
		if statErr != nil {
			// Vanished mid-walk. Treat as unknown-but-present rather than lying.
			res.Changes = append(res.Changes, Change{Path: path, Kind: Modified})
			return nil
		}
		if info.Size() != want.Size {
			// Size differs: dirty, and we never had to open the file.
			res.Changes = append(res.Changes, Change{Path: path, Kind: Modified})
			return nil
		}
		// Same size: the only case that forces a read. Hash just this file.
		res.Hashed++
		sum, herr := hashFile(path)
		if herr != nil {
			res.Changes = append(res.Changes, Change{Path: path, Kind: Modified})
			return nil
		}
		if !strings.EqualFold(sum, want.Digest) {
			res.Changes = append(res.Changes, Change{Path: path, Kind: Modified})
		}
		return nil
	})
	if walkErr != nil {
		return Result{}, walkErr
	}

	// Anything the server has that we never saw on disk is a local delete.
	for key, want := range depot {
		if _, ok := seen[key]; ok {
			continue
		}
		_ = want
		res.Changes = append(res.Changes, Change{Path: key, Kind: Deleted})
	}

	sort.Slice(res.Changes, func(i, j int) bool {
		return res.Changes[i].Path < res.Changes[j].Path
	})
	res.Took = time.Since(start)
	res.At = time.Now()
	return res, nil
}

// hashFile returns the uppercase MD5 hex of a file's raw bytes.
//
// Note: for text filetypes Perforce stores the digest of the server-normalized
// content (line endings), so a text file with foreign line endings can hash
// differently here even when Perforce would call it unchanged. Size-based
// detection is unaffected. This normalization gap is listed in the README.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(h.Sum(nil))), nil
}
