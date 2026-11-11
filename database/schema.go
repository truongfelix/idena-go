package database

var (
	SnapshotDbPrefix = []byte("snpsht")

	// headBlockKey tracks the latest know full block's hash.
	headBlockKey = []byte("LastBlock")

	headerPrefix = []byte("h")

	headerHashSuffix = []byte("n") // headerPrefix + num (uint64 big endian) + headerHashSuffix -> hash

	finalConsensusPrefix = []byte("f")

	transactionIndexPrefix = []byte("ti")

	receiptIndexPrefix = []byte("ri")

	ownTransactionIndexPrefix = []byte("oti")

	burntCoinsPrefix = []byte("bc")

	certPrefix = []byte("c")

	flipEncryptionPrefix = []byte("key")

	weakCertificatesKey = []byte("weak-cert")

	lastSnapshotKey = []byte("last-snapshot")

	identityStateDiffPrefix = []byte("id-diff")

	preliminaryHeadKey = []byte("preliminary-head")

	activityMonitorKey = []byte("activity")

	eventPrefix = []byte("e")

	intermediateGenesisKey = []byte("g")

	preliminaryIntermediateGenesisKey = []byte("pg")

	upgradeVotesKey = []byte("uv")

	consensusVersionKey = []byte("v")

	preliminaryConsVersionKey = []byte("pv")

	// consensusUpgradeHeightKey holds the height of the block that brought the stored consensus version (the rules
	// change after it); preliminaryConsUpgradeHeightKey the same for the chain a fast sync downloads.
	consensusUpgradeHeightKey = []byte("vh")

	preliminaryConsUpgradeHeightKey = []byte("pvh")

	applyTxLogPrefix = []byte("applytxlog")

	blackListedTxPrefix = []byte("blacktx")
)
