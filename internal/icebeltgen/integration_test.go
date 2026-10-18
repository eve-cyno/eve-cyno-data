//go:build integration

package main

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/sde/sdetest"
	"eve-cyno.dev/go/data/version"
)

func TestIntegration_WikiPagesStillParse(t *testing.T) {
	db, err := openSDE(sdetest.Path(t))
	require.NoError(t, err)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pages, err := fetchPages(ctx, &http.Client{Timeout: 30 * time.Second}, esi.ProjectUserAgent(version.Version(), os.Getenv), time.Second)
	require.NoError(t, err)
	require.Len(t, pages, len(beltTitles()))

	total := 0
	for _, p := range pages {
		total += len(p.Entries)
	}
	require.GreaterOrEqual(t, total, 100)

	ds, unresolved := buildDataset(pages, sdeLookup(db))
	require.Empty(t, unresolved, "every wiki name resolves after nameFixes")
	require.GreaterOrEqual(t, len(ds.Systems), 100)
}
