package node

// REPLAY TEST HARNESS ONLY (write-buffer A/B, notes/write-buffer-ab-plan.md).

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/log"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/tendermint/tm-db"
)

// HarnessOpenForConsensusVersion opens the chain DB for the short consensus-version read at start (main.go).
// IDENA_HARNESS_SHORT_OPEN: "default" = 4 MiB write buffer whatever IDENA_DB_WRITEBUFFER_MB says (the code with the
// write-buffer option: that open runs before the flags are applied); "ro" = read-only (option A: nothing written, a
// missing database = no stored version: nil, nil); unset = OpenDatabase (the env size, like the main open).
func HarnessOpenForConsensusVersion(datadir string) (db.DB, error) {
	switch os.Getenv("IDENA_HARNESS_SHORT_OPEN") {
	case "default":
		return openDatabaseHarness(datadir, "idenachain", 16, 16, 16/4*opt.MiB, false, false)
	case "ro":
		d, err := openDatabaseHarness(datadir, "idenachain", 16, 16, 16/4*opt.MiB, true, false)
		if err != nil && os.IsNotExist(err) {
			log.Info("HARNESS: no chain DB yet, no stored consensus version")
			return nil, nil
		}
		return d, err
	default:
		return OpenDatabase(datadir, "idenachain", 16, 16, false)
	}
}

// startHarnessDbStats appends the chain DB's goleveldb counters to IDENA_DBSTATS every 30 s, plus marked dumps:
// START before the first block the full sync applies and END after block IDENA_HARNESS_END_HEIGHT, both once the
// DB is idle (pending compactions belong to the blocks before them). After END the full sync stops applying blocks.
// IDENA_HARNESS_DB_IDLE=1 also waits for an idle DB before every block, like a node at the tip (one block per
// round, compactions finish in between); without it blocks are applied as fast as they come (catch-up).
func startHarnessDbStats(ldb *leveldb.DB, statsPath string) {
	dump := func(marker string) {
		stats, _ := ldb.GetProperty("leveldb.stats")
		io, _ := ldb.GetProperty("leveldb.iostats")
		count, _ := ldb.GetProperty("leveldb.compcount")
		delay, _ := ldb.GetProperty("leveldb.writedelay")
		if f, err := os.OpenFile(statsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
			fmt.Fprintf(f, "=== %v%s\n%s\n%s\n%s\n%s\n", time.Now().Unix(), marker, stats, io, count, delay)
			f.Close()
		}
	}
	dump(" OPEN height=0") // right after the main open: the start-time compaction is counted from here
	go func() {
		for {
			time.Sleep(30 * time.Second)
			dump("")
		}
	}()

	paced := os.Getenv("IDENA_HARNESS_DB_IDLE") == "1"
	endHeight, _ := strconv.ParseUint(os.Getenv("IDENA_HARNESS_END_HEIGHT"), 10, 64)
	log.Info("HARNESS: db write hooks", "paced", paced, "endHeight", endHeight)
	started := false
	var waited time.Duration
	common.HarnessBeforeBlock = func(height uint64) {
		if !started {
			waitDbIdle(ldb)
			dump(fmt.Sprintf(" START height=%d", height))
			started = true
			return
		}
		if paced {
			t := time.Now()
			waitDbIdle(ldb)
			waited += time.Since(t)
		}
	}
	common.HarnessAfterBlock = func(height uint64) {
		if endHeight == 0 || height != endHeight {
			return
		}
		t := time.Now()
		waitDbIdle(ldb)
		dump(fmt.Sprintf(" END height=%d pacedWaitMs=%d settleMs=%d", height, waited.Milliseconds(),
			time.Since(t).Milliseconds()))
		log.Info("HARNESS: END dumped, full sync stops here", "height", height)
		select {}
	}
}

var levelRow = regexp.MustCompile(`^\s*(\d+)\s*\|\s*(\d+)\s*\|\s*([\d.]+)\s*\|`)

// waitDbIdle returns once goleveldb has nothing left to compact: fewer level-0 tables than the compaction
// trigger, every deeper level under its size limit, and no byte written for 1 s. Gives up after 10 minutes.
func waitDbIdle(ldb *leveldb.DB) {
	deadline := time.Now().Add(10 * time.Minute)
	var last string
	stable := 0
	for time.Now().Before(deadline) {
		stats, _ := ldb.GetProperty("leveldb.stats")
		io, _ := ldb.GetProperty("leveldb.iostats")
		pending := false
		for _, line := range strings.Split(stats, "\n") {
			m := levelRow.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			level, _ := strconv.Atoi(m[1])
			tables, _ := strconv.Atoi(m[2])
			sizeMB, _ := strconv.ParseFloat(m[3], 64)
			if level == 0 && tables >= opt.DefaultCompactionL0Trigger {
				pending = true
			}
			limit := float64(opt.DefaultCompactionTotalSize) / opt.MiB
			for i := 0; i < level; i++ {
				limit *= opt.DefaultCompactionTotalSizeMultiplier
			}
			if level > 0 && sizeMB >= limit {
				pending = true
			}
		}
		if !pending && io == last {
			stable++
		} else {
			stable = 0
		}
		if stable >= 5 {
			return
		}
		last = io
		time.Sleep(200 * time.Millisecond)
	}
	log.Warn("HARNESS: chain DB not idle after 10 minutes, going on")
}
