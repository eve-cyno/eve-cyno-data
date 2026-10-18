// Command icebeltgen derives core/icebelts/ice_systems.json from the four EVE University
// Wiki pages that list the ice systems of the faction ice belts (Clear Icicle Belt, White
// Glaze Belt, Blue Ice Belt, Glacial Mass Belt), checking every system name against the SDE.
//
//	go run ./internal/icebeltgen -out icebelts/ice_systems.json
//
// It fetches over the network (one request at a time, one second apart, with the project
// User-Agent), so there is no go:generate directive. -from-dir reads "<Title_with_underscores>.json"
// API answers from a directory instead of fetching. The SDE path is -sde, else
// EVE_CORE_SDE_PATH, else data/sde/sde.sqlite found upward. A name that does not resolve
// in the SDE (after the known spelling fixes) is listed and the command exits 1.
//
// The wiki text is CC BY-SA 4.0, so the output file is too; see the icebelts package doc.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"eve-cyno.dev/go/data/config"
	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/version"
)

const (
	apiURL       = wikiBase + "api.php"
	fetchGap     = time.Second
	maxPageBytes = 4 << 20
)

func main() {
	out := flag.String("out", "icebelts/ice_systems.json", "output JSON path")
	sdePath := flag.String("sde", "", "SDE SQLite path (default EVE_CORE_SDE_PATH / data/sde/sde.sqlite)")
	fromDir := flag.String("from-dir", "", "read <Title_with_underscores>.json API answers from this directory instead of fetching")
	flag.Parse()
	if err := run(*sdePath, *fromDir, *out); err != nil {
		fmt.Fprintln(os.Stderr, "icebeltgen:", err)
		os.Exit(1)
	}
}

func run(sdePath, fromDir, out string) error {
	if sdePath == "" {
		sdePath = config.Load().SDEPath
	}
	db, err := openSDE(sdePath)
	if err != nil {
		return err
	}
	defer db.Close()

	var pages []page
	if fromDir != "" {
		pages, err = readPages(fromDir)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		pages, err = fetchPages(ctx, &http.Client{Timeout: 30 * time.Second}, esi.ProjectUserAgent(version.Version(), os.Getenv), fetchGap)
	}
	if err != nil {
		return err
	}

	ds, unresolved := buildDataset(pages, sdeLookup(db))
	if len(unresolved) > 0 {
		return fmt.Errorf("%d names not in the SDE: %s", len(unresolved), strings.Join(unresolved, ", "))
	}
	data, err := encode(ds)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}
	total := 0
	for _, p := range pages {
		total += len(p.Entries)
	}
	fmt.Printf("%d listed entries -> %d systems -> %s\n", total, len(ds.Systems), out)
	return nil
}

// openSDE opens the SDE SQLite read-only.
func openSDE(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("SDE %s: %w", path, err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open SDE: %w", err)
	}
	return db, nil
}

// sdeLookup resolves a system name (exact spelling) to its ID, security and region.
func sdeLookup(db *sql.DB) func(string) (int, float64, string, bool) {
	return func(name string) (int, float64, string, bool) {
		var id int
		var sec float64
		var region string
		err := db.QueryRow(`SELECT s.solarSystemID, s.security, COALESCE(r.regionName, '')
			FROM mapSolarSystems s LEFT JOIN mapRegions r ON r.regionID = s.regionID
			WHERE s.solarSystemName = ?`, name).Scan(&id, &sec, &region)
		if err != nil {
			return 0, 0, "", false
		}
		return id, sec, region, true
	}
}

// parseAnswer reads one action=parse answer (formatversion=2) into a page.
func parseAnswer(body []byte, requested string) (page, error) {
	var ans struct {
		Parse struct {
			Title    string `json:"title"`
			RevID    int    `json:"revid"`
			Wikitext string `json:"wikitext"`
		} `json:"parse"`
		Error *struct {
			Info string `json:"info"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &ans); err != nil {
		return page{}, fmt.Errorf("decode answer: %w", err)
	}
	if ans.Error != nil {
		return page{}, fmt.Errorf("wiki error: %s", ans.Error.Info)
	}
	if ans.Parse.Title == "" || ans.Parse.Wikitext == "" {
		return page{}, errors.New("answer has no page text")
	}
	if strings.ReplaceAll(ans.Parse.Title, "_", " ") != requested {
		return page{}, fmt.Errorf("wiki returned page %q for requested %q (redirect or rename)", ans.Parse.Title, requested)
	}
	return page{
		Title:    requested,
		Revision: ans.Parse.RevID,
		Entries:  parseBeltPage(requested, ans.Parse.Wikitext),
	}, nil
}

// fetchPages reads the belt pages from the wiki, one request at a time, gap apart.
func fetchPages(ctx context.Context, client *http.Client, userAgent string, gap time.Duration) ([]page, error) {
	var pages []page
	for i, title := range beltTitles() {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("fetch %s: %w", title, ctx.Err())
			case <-time.After(gap):
			}
		}
		q := url.Values{}
		q.Set("action", "parse")
		q.Set("page", strings.ReplaceAll(title, " ", "_"))
		q.Set("prop", "wikitext|revid")
		q.Set("format", "json")
		q.Set("formatversion", "2")
		q.Set("redirects", "1")
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL+"?"+q.Encode(), nil)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", title, err)
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", title, err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", title, err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetch %s: HTTP %d", title, resp.StatusCode)
		}
		p, err := parseAnswer(body, title)
		if err != nil {
			return nil, fmt.Errorf("page %s: %w", title, err)
		}
		pages = append(pages, p)
	}
	return pages, nil
}

// readPages reads saved API answers named <Title_with_underscores>.json from dir.
func readPages(dir string) ([]page, error) {
	var pages []page
	for _, title := range beltTitles() {
		path := filepath.Join(dir, strings.ReplaceAll(title, " ", "_")+".json")
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		p, err := parseAnswer(body, title)
		if err != nil {
			return nil, fmt.Errorf("page %s: %w", title, err)
		}
		pages = append(pages, p)
	}
	return pages, nil
}
