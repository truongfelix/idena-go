// REPLAY TEST HARNESS ONLY (write-buffer A/B, notes/write-buffer-ab-plan.md): times opening a chain DB left by a
// node that was stopped without a clean close (the journal is replayed into the memtable, flushed to level 0 each
// time it reaches the write buffer). Options as node.OpenDatabase(…, 16, 16) with the write buffer given.
//
//	dbopen <idenachain.db dir> <write buffer MiB>
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/filter"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: dbopen <idenachain.db dir> <write buffer MiB>")
		os.Exit(2)
	}
	dir := os.Args[1]
	mb, err := strconv.Atoi(os.Args[2])
	if err != nil || mb <= 0 {
		fmt.Fprintln(os.Stderr, "bad write buffer:", os.Args[2])
		os.Exit(2)
	}
	journals, journalBytes := 0, int64(0)
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".log") {
			if info, err := os.Stat(filepath.Join(dir, e.Name())); err == nil {
				journals++
				journalBytes += info.Size()
			}
		}
	}
	start := time.Now()
	db, err := leveldb.OpenFile(dir, &opt.Options{
		OpenFilesCacheCapacity: 16,
		BlockCacheCapacity:     8 * opt.MiB,
		WriteBuffer:            mb * opt.MiB,
		Filter:                 filter.NewBloomFilter(10),
	})
	elapsed := time.Since(start)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	count, _ := db.GetProperty("leveldb.compcount")
	io, _ := db.GetProperty("leveldb.iostats")
	db.Close()
	fmt.Printf("writeBufferMiB=%d journals=%d journalMiB=%.1f openMs=%d %s %s\n", mb, journals,
		float64(journalBytes)/opt.MiB, elapsed.Milliseconds(), strings.TrimSpace(count), strings.TrimSpace(io))
}
