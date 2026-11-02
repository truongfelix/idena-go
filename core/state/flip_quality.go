package state

import "github.com/idena-network/idena-go/common"

// FlipQualityCeremonies is how many of an identity's ceremonies with flips its flip quality window keeps (v13).
const FlipQualityCeremonies = 10

// maxFlipQualityFlips bounds the flips counted for one ceremony, so that the points (2 per flip at most) fit in a
// byte. No identity can make that many flips: the bound only keeps the encoding total.
const maxFlipQualityFlips = 127

// FlipQuality returns the identity's flip quality window summed: the flips made and their points x2 over its last
// FlipQualityCeremonies ceremonies with flips. An empty window returns 0, 0.
func (i Identity) FlipQuality() (flips, points2 uint32) {
	return sumFlipQuality(i.flipQuality)
}

// FlipQualityWindow returns a copy of the window's raw pairs (flips made, points x2), oldest first.
func (i Identity) FlipQualityWindow() []byte {
	return append([]byte(nil), i.flipQuality...)
}

func sumFlipQuality(window []byte) (flips, points2 uint32) {
	for idx := 0; idx+1 < len(window); idx += 2 {
		flips += uint32(window[idx])
		points2 += uint32(window[idx+1])
	}
	return flips, points2
}

// appendFlipQuality adds one ceremony's pair to a window and drops the oldest pairs beyond FlipQualityCeremonies.
// A ceremony without flips adds nothing. points2 is capped at twice the flips.
func appendFlipQuality(window []byte, flips, points2 uint32) []byte {
	if flips == 0 {
		return window
	}
	if flips > maxFlipQualityFlips {
		flips = maxFlipQualityFlips
	}
	if points2 > 2*flips {
		points2 = 2 * flips
	}
	res := make([]byte, 0, len(window)+2)
	res = append(res, window...)
	res = append(res, byte(flips), byte(points2))
	if extra := len(res) - 2*FlipQualityCeremonies; extra > 0 {
		res = res[extra:]
	}
	return res
}

func (s *stateIdentity) AddFlipQuality(flips, points2 uint32) {
	if flips == 0 {
		return
	}
	s.data.flipQuality = appendFlipQuality(s.data.flipQuality, flips, points2)
	s.touch()
}

// AddFlipQuality records one ceremony's result of addr's own flips in its v13 window.
func (s *StateDB) AddFlipQuality(addr common.Address, flips, points2 uint32) {
	s.GetOrNewIdentityObject(addr).AddFlipQuality(flips, points2)
}

// GetFlipQuality returns addr's window summed (flips made, points x2); 0, 0 when it has none.
func (s *StateDB) GetFlipQuality(addr common.Address) (flips, points2 uint32) {
	stateObject := s.getStateIdentity(addr)
	if stateObject == nil {
		return 0, 0
	}
	return stateObject.data.FlipQuality()
}
