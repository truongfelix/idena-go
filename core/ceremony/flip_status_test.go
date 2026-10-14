package ceremony

import (
	"testing"

	"github.com/idena-network/idena-go/stats/validationsummary"
	"github.com/stretchr/testify/require"
)

// validationsummary counts the ceremony's flips by these values without importing this package.
func Test_FlipStatusValues(t *testing.T) {
	require.Equal(t, byte(NotQualified), byte(validationsummary.FlipNotQualified))
	require.Equal(t, byte(Qualified), byte(validationsummary.FlipQualified))
	require.Equal(t, byte(WeaklyQualified), byte(validationsummary.FlipWeaklyQualified))
	require.Equal(t, byte(QualifiedByNone), byte(validationsummary.FlipQualifiedByNone))
}
