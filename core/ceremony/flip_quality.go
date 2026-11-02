package ceremony

import (
	"github.com/idena-network/idena-go/blockchain/types"
	"github.com/idena-network/idena-go/common"
)

// flipQualityPoints2 is a flip's points x2 for its author's v13 flip quality window: Qualified and WeaklyQualified
// 1, NotQualified 0.5 (also a flip nobody answered), QualifiedByNone 0, and 0 for a flip the long session reported,
// whatever its status.
func flipQualityPoints2(item FlipQualification) uint32 {
	if item.grade == types.GradeReported {
		return 0
	}
	switch item.status {
	case Qualified, WeaklyQualified:
		return 2
	case NotQualified:
		return 1
	default:
		return 0
	}
}

// authorFlipQuality is one ceremony's result of an author's own flips: the flips it made and their points x2.
type authorFlipQuality struct {
	flips   uint32
	points2 uint32
}

// flipQualityByAuthor sums each author's flips of the shard (flipsByAuthor: indexes into qualifications).
func flipQualityByAuthor(qualifications []FlipQualification, flipsByAuthor map[common.Address][]int) map[common.Address]authorFlipQuality {
	res := make(map[common.Address]authorFlipQuality, len(flipsByAuthor))
	for author, flipIdxs := range flipsByAuthor {
		var q authorFlipQuality
		for _, flipIdx := range flipIdxs {
			q.flips++
			q.points2 += flipQualityPoints2(qualifications[flipIdx])
		}
		res[author] = q
	}
	return res
}
