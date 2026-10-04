package node

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

func TestHarnessWaitDbIdle(t *testing.T) {
	// Tiny write buffer and compactions disabled at level 0 (trigger far away): flushes pile up in level 0.
	db, err := leveldb.OpenFile(t.TempDir(), &opt.Options{WriteBuffer: 64 * 1024, CompactionL0Trigger: 1000,
		WriteL0SlowdownTrigger: 1000, WriteL0PauseTrigger: 1000})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	start := time.Now()
	waitDbIdle(db)
	if d := time.Since(start); d < time.Second || d > 3*time.Second {
		t.Fatalf("empty DB: waited %v, want ~1 s (no write for 1 s)", d)
	}

	value := make([]byte, 1024)
	for i := 0; i < 2000; i++ {
		if err := db.Put([]byte(fmt.Sprintf("key%06d", i)), value, nil); err != nil {
			t.Fatal(err)
		}
	}
	stats, _ := db.GetProperty("leveldb.stats")
	pending := false
	for _, line := range splitLines(stats) {
		if m := levelRow.FindStringSubmatch(line); m != nil && m[1] == "0" {
			tables, _ := strconv.Atoi(m[2])
			pending = tables >= 4
		}
	}
	if !pending {
		t.Fatalf("setup: expected >= 4 level-0 tables, stats:\n%s", stats)
	}
	done := make(chan struct{})
	go func() {
		waitDbIdle(db)
		close(done)
	}()
	select {
	case <-done:
		t.Fatalf("returned while level 0 is over the trigger, stats:\n%s", stats)
	case <-time.After(3 * time.Second):
	}
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := range s {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	return append(lines, s[start:])
}

func TestHarnessReadOnlyShortOpen(t *testing.T) {
	t.Setenv("IDENA_HARNESS_SHORT_OPEN", "ro")
	dir := t.TempDir()

	// No database yet: no error, nothing created.
	d, err := HarnessOpenForConsensusVersion(dir)
	if err != nil || d != nil {
		t.Fatalf("fresh datadir: got %v, %v; want nil, nil", d, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("fresh datadir: read-only open created %d entries", len(entries))
	}

	// A value only in the journal (closed without a flush, like a killed node): seen, and no file changes.
	w, err := OpenDatabase(dir, "idenachain", 16, 16, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set([]byte("consensus"), []byte("v12")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	before := listing(t, dir+"/idenachain.db")
	d, err = HarnessOpenForConsensusVersion(dir)
	if err != nil || d == nil {
		t.Fatalf("existing database: got %v, %v", d, err)
	}
	if v, err := d.Get([]byte("consensus")); err != nil || string(v) != "v12" {
		t.Fatalf("journal value: got %q, %v", v, err)
	}
	d.Close()
	if after := listing(t, dir+"/idenachain.db"); after != before {
		t.Fatalf("read-only open changed files:\nbefore %s\nafter  %s", before, after)
	}
}

func listing(t *testing.T, dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, e := range entries {
		if e.Name() == "LOG" || e.Name() == "LOG.old" { // goleveldb's own text log
			continue
		}
		info, _ := e.Info()
		s = append(s, fmt.Sprintf("%s:%d", e.Name(), info.Size()))
	}
	return strings.Join(s, " ")
}
