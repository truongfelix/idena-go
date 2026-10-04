package common

// REPLAY TEST HARNESS ONLY (write-buffer A/B, notes/write-buffer-ab-plan.md): the full sync calls these around
// each block it applies; node sets them when IDENA_DBSTATS is on.
var (
	HarnessBeforeBlock func(height uint64)
	HarnessAfterBlock  func(height uint64)
)
