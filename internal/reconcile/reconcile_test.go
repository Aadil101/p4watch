package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Aadil101/p4watch/internal/p4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFile creates path (and parents) with content and returns its byte length.
func writeFile(t *testing.T, path, content string) int64 {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return int64(len(content))
}

// depotOf builds the server view for a file exactly as Perforce would report it:
// the true size and the uppercase MD5 of the on-disk content. Callers then
// tweak size/digest to simulate a file that has drifted from the depot.
func depotOf(t *testing.T, path string) p4.DepotFile {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	sum, err := hashFile(path)
	require.NoError(t, err)
	return p4.DepotFile{Size: info.Size(), Digest: sum}
}

func key(path string) string { return strings.ToLower(path) }

// gotSet flattens a Result's changes into a comparable path->kind map.
func gotSet(r Result) map[string]string {
	m := make(map[string]string, len(r.Changes))
	for _, ch := range r.Changes {
		m[strings.ToLower(ch.Path)] = ch.Kind.String()
	}
	return m
}

func TestDiff(t *testing.T) {
	root := t.TempDir()

	// A clean file: on disk, matches depot size+digest exactly.
	cleanPath := filepath.Join(root, "clean.txt")
	writeFile(t, cleanPath, "unchanged contents")

	// Modified by size: on-disk length differs from depot, so it must be caught
	// by stat alone and never hashed.
	sizePath := filepath.Join(root, "sized.txt")
	writeFile(t, sizePath, "now this file is much longer than the depot thinks")

	// Modified same-size: identical length, different bytes. Only stat can't
	// settle this, so it is the one modified case that forces a hash.
	samePath := filepath.Join(root, "same.txt")
	writeFile(t, samePath, "AAAAAAAAAA") // 10 bytes

	// Added: on disk, absent from depot entirely.
	addPath := filepath.Join(root, "added.txt")
	writeFile(t, addPath, "brand new")

	// Case-fold: created with mixed case on disk, depot keyed lowercase. On NTFS
	// these are the same file and must reconcile clean, not show up as add+delete.
	casePath := filepath.Join(root, "MixedCase.txt")
	writeFile(t, casePath, "case insensitive")

	// A file inside a .p4 metadata dir must be skipped, not walked.
	p4Path := filepath.Join(root, ".p4tmp", "ignore.txt")
	writeFile(t, p4Path, "server metadata, not workspace content")

	depot := map[string]p4.DepotFile{
		key(cleanPath): depotOf(t, cleanPath),
		key(casePath):  depotOf(t, casePath),

		// Same length as on disk, but we lie about the size by one byte so the
		// diff must flag it without opening the file.
		key(sizePath): {Size: depotOf(t, sizePath).Size + 1, Digest: depotOf(t, sizePath).Digest},

		// Same size (10 bytes), different digest: forces a hash, ends up Modified.
		key(samePath): {Size: 10, Digest: strings.Repeat("B", 32)},

		// In depot, but nothing on disk maps to it: a local delete.
		key(filepath.Join(root, "gone.txt")): {Size: 5, Digest: strings.Repeat("C", 32)},
	}

	res, err := diff(context.Background(), root, depot)
	require.NoError(t, err)

	want := map[string]string{
		key(sizePath):                        "edit",
		key(samePath):                        "edit",
		key(addPath):                         "add",
		key(filepath.Join(root, "gone.txt")): "delete",
	}
	assert.Equal(t, want, gotSet(res), "dirty set")

	// clean.txt and MixedCase.txt must NOT appear.
	assert.NotContains(t, gotSet(res), key(cleanPath), "clean file reported dirty")
	assert.NotContains(t, gotSet(res), key(casePath), "case-folded file reported dirty")

	// Counters. Walked = the 5 real files under root; the .p4tmp file is skipped.
	assert.Equal(t, 5, res.Walked, "Walked (excludes .p4tmp)")
	assert.Equal(t, len(depot), res.DepotFiles, "DepotFiles")
	// Only files whose size matched the depot get hashed: clean, same, MixedCase.
	// The size-mismatch file must NOT be hashed; Added has no depot entry.
	assert.Equal(t, 3, res.Hashed, "Hashed (clean, same-size-diff, case-fold)")
}

func TestDiffSorted(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"zeta.txt", "alpha.txt", "mid.txt"} {
		writeFile(t, filepath.Join(root, name), "x")
	}
	// Empty depot: every file is Added, so we can assert purely on ordering.
	res, err := diff(context.Background(), root, map[string]p4.DepotFile{})
	require.NoError(t, err)

	paths := make([]string, len(res.Changes))
	for i, ch := range res.Changes {
		paths[i] = ch.Path
	}
	assert.True(t, sort.SliceIsSorted(paths, func(i, j int) bool { return paths[i] < paths[j] }),
		"changes sorted by path: %v", paths)
}

func TestDiffCleanWorkspace(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "only.txt")
	writeFile(t, p, "matches the server")
	depot := map[string]p4.DepotFile{key(p): depotOf(t, p)}

	res, err := diff(context.Background(), root, depot)
	require.NoError(t, err)
	assert.Zero(t, res.Count(), "clean workspace should report no changes: %v", res.Changes)
}
