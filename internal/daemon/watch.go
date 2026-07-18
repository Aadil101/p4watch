package daemon

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/fsnotify/fsnotify"
)

// treeWatcher watches a workspace tree for changes and turns them into two
// signals the daemon acts on:
//
//   - pokes: an ordinary change happened. The daemon coalesces these and does a
//     debounced re-anchor. Events are only a TRIGGER here, never the source of
//     truth: the re-anchor is a full reconcile, so even a change the watch races
//     (a file dropped into a brand-new directory before its watch is added) is
//     still caught by the pass the poke triggers.
//   - force: something happened that means we may have MISSED changes (fsnotify
//     reported a buffer overflow / dropped events). We cannot trust that a poke
//     will arrive for whatever we lost, so we demand an immediate re-anchor to
//     close the window instead of waiting for the debounce or the backstop.
//
// fsnotify is not recursive on any platform, so every directory gets its own
// watch; directories created later are watched as their Create event arrives.
// On Windows each watch owns a ReadDirectoryChangesW buffer, so a very large
// tree is memory-heavy: dropping to a single bWatchSubtree watch is possible
// later, but per-directory watches are what the benchmark proved out.
type treeWatcher struct {
	w     *fsnotify.Watcher
	root  string
	pokes chan struct{}
	force chan struct{}

	dropped atomic.Int64
	dirs    atomic.Int64
}

// newTreeWatcher creates a watcher and adds a watch for every directory under
// root (skipping Perforce metadata dirs, which would only generate self-noise).
// The startup walk is a cold-start cost, not on any query path.
func newTreeWatcher(root string) (*treeWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	tw := &treeWatcher{
		w:    w,
		root: root,
		// Buffered depth 1: the channels carry "there is work", not a count. A
		// pending signal already means "re-anchor", so extra ones coalesce away.
		pokes: make(chan struct{}, 1),
		force: make(chan struct{}, 1),
	}
	if err := tw.addTree(root); err != nil {
		w.Close()
		return nil, err
	}
	log.Printf("watching %d dirs under %s", tw.dirs.Load(), root)
	return tw, nil
}

// addTree adds a watch for root and every directory beneath it.
func (tw *treeWatcher) addTree(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip it, don't abort the walk
		}
		if !d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".p4") {
			return filepath.SkipDir
		}
		tw.addDir(path)
		return nil
	})
}

func (tw *treeWatcher) addDir(path string) {
	if err := tw.w.Add(path); err != nil {
		log.Printf("watch %s: %v", path, err)
		return
	}
	tw.dirs.Add(1)
}

// run consumes fsnotify's channels until ctx is cancelled or the watcher closes,
// translating raw events into pokes and drops into forced re-anchors.
func (tw *treeWatcher) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-tw.w.Events:
			if !ok {
				return
			}
			// A newly created directory needs its own watch or its contents are
			// invisible to the event stream. Any child that lands before the
			// watch is added is still caught by the full re-anchor this poke
			// triggers, so the race is a latency concern, not a correctness one.
			if ev.Has(fsnotify.Create) {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					tw.addDir(ev.Name)
				}
			}
			tw.signal(tw.pokes)
		case err, ok := <-tw.w.Errors:
			if !ok {
				return
			}
			// On Windows this is ReadDirectoryChangesW buffer overflow: events
			// were dropped, so we may not get a poke for a real change. Force a
			// re-anchor rather than trust an event stream we know has a hole.
			tw.dropped.Add(1)
			log.Printf("fsnotify dropped events, forcing re-anchor: %v", err)
			tw.signal(tw.force)
		}
	}
}

// signal does a non-blocking send: if a signal is already pending, this one
// coalesces into it. The receiver re-anchors once regardless of how many events
// piled up, which is exactly the collapsing we want.
func (tw *treeWatcher) signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (tw *treeWatcher) Close() error { return tw.w.Close() }
