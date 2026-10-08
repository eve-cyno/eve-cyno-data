package diff

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSigStable(t *testing.T) {
	u, _ := url.Parse("https://esi.evetech.net/latest/markets/10000002/orders/?type_id=34&page=1")
	require.Equal(t,
		Sig("GET", u, nil),
		"get /latest/markets/10000002/orders/?page=1&type_id=34|e3b0c44298fc1c14")
}
