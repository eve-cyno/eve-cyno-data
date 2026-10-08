package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"eve-cyno.dev/go/data/esi"
)

// appraiseItems mirrors Python _appraise_items (Janice). [OPUS-REVIEW] cassette pending.
func appraiseItems(ctx context.Context, c *Client, items string, janiceAPIKey string) (string, error) {
	if janiceAPIKey == "" {
		return "Janice API key not configured.", nil
	}

	url := JaniceBase + "/api/rest/v1/appraisal"
	rel, err := c.Throttle.Acquire(ctx, url)
	if err != nil {
		return "", err
	}
	defer rel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(items))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-ApiKey", janiceAPIKey)
	req.Header.Set("accept-encoding", "")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Sprintf("Janice appraisal request failed: %v", err), nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Sprintf("Janice error %d: %s", resp.StatusCode, string(body)), nil
	}

	var result struct {
		Code       string  `json:"code"`
		BuyTotal   float64 `json:"buyTotal"`
		SellTotal  float64 `json:"sellTotal"`
		SplitTotal float64 `json:"splitTotal"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "Failed to parse Janice appraisal response.", nil
	}

	url2 := ""
	if result.Code != "" {
		url2 = JaniceBase + "/" + result.Code
	}

	lines := []string{
		"**Janice Appraisal:**",
		fmt.Sprintf("  Buy: %.2f ISK", result.BuyTotal),
		fmt.Sprintf("  Sell: %.2f ISK", result.SellTotal),
		fmt.Sprintf("  Split: %.2f ISK", result.SplitTotal),
	}
	if url2 != "" {
		lines = append(lines, fmt.Sprintf("Full appraisal: %s", url2))
	}
	return strings.Join(lines, "\n"), nil
}

// fallbackUSDToEUR is used when the Frankfurter call fails or returns nothing usable.
const fallbackUSDToEUR = 0.92

// usdToEURRate returns the current USD->EUR rate from Frankfurter v2
// (GET /v2/rate/USD/EUR -> {"date","base","quote","rate"}), or fallbackUSDToEUR
// on any failure (HTTP error, bad JSON, missing or non-positive rate).
func usdToEURRate(ctx context.Context, c *Client) float64 {
	body, status, err := doGet(ctx, c, FrankfurterBase+"/rate/USD/EUR")
	if err != nil || status != http.StatusOK {
		return fallbackUSDToEUR
	}
	var fx struct {
		Rate float64 `json:"rate"`
	}
	if err := json.Unmarshal(body, &fx); err != nil || fx.Rate <= 0 {
		return fallbackUSDToEUR
	}
	return fx.Rate
}

// PLEXRegion is the market region PLEX trades in: the global PLEX market (region
// 19000001, "PLEX" in the SDE), separate from the empire regions since the 2020 PLEX
// market change. The Forge holds no PLEX order, so a read of ForgeRegion answers an
// empty book.
const PLEXRegion = 19000001

// convertISKToReal mirrors Python _convert_isk_to_real, reading the PLEX price from the
// global PLEX market (PLEXRegion).
func convertISKToReal(ctx context.Context, c *Client, iskAmount float64, currency string) (string, error) {
	// Get PLEX price in ISK via ESI
	resp, err := c.esiClient().Do(ctx, esi.Request{
		Path:  fmt.Sprintf("/markets/%d/orders", PLEXRegion),
		Query: url.Values{"type_id": {"44992"}, "order_type": {"sell"}},
	})
	if err != nil || resp.Status != 200 {
		return "Could not fetch PLEX price for ISK→real conversion.", nil
	}
	body := resp.Body

	var orders []struct {
		Price float64 `json:"price"`
	}
	plexISK := 0.0
	if err := json.Unmarshal(body, &orders); err == nil && len(orders) > 0 {
		minP := orders[0].Price
		for _, o := range orders[1:] {
			if o.Price < minP {
				minP = o.Price
			}
		}
		plexISK = minP
	}
	if plexISK == 0 {
		return "Could not determine PLEX price.", nil
	}

	usdToEur := usdToEURRate(ctx, c)

	// 500 PLEX = $20 USD (CCP pricing)
	plexPerUSD := plexISK * 500.0 / 20.0
	iskInUSD := iskAmount / plexPerUSD
	iskInEUR := iskInUSD * usdToEur

	lines := []string{
		fmt.Sprintf("**ISK → Real Money** (%.0f ISK at PLEX rate):", iskAmount),
		fmt.Sprintf("  PLEX price: %.2f ISK", plexISK),
		fmt.Sprintf("  Effective rate: %.2f ISK/USD", plexPerUSD),
		"",
	}
	switch strings.ToUpper(currency) {
	case "USD":
		lines = append(lines, fmt.Sprintf("  ≈ $%.2f USD", iskInUSD))
	case "EUR":
		lines = append(lines, fmt.Sprintf("  ≈ €%.2f EUR", iskInEUR))
	default: // BOTH
		lines = append(lines, fmt.Sprintf("  ≈ $%.2f USD / €%.2f EUR", iskInUSD, iskInEUR))
	}
	return strings.Join(lines, "\n"), nil
}
