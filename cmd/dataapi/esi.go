package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"eve-cyno.dev/go/data/dataapi"
	"eve-cyno.dev/go/data/esi"
	"eve-cyno.dev/go/data/tools"
)

const (
	// defaultUpstreamToolLimit is DATAAPI_UPSTREAM_TOOL_LIMIT: calls per minute per caller of
	// the tools that read live ESI data (dataapi.DefaultLimits().UpstreamTool).
	defaultUpstreamToolLimit = 10
	// defaultESIGuardPercent is DATAAPI_ESI_GUARD_PERCENT: the share of every ESI rate-limit
	// group's tokens kept unspent, on top of which the client answers "busy" instead of
	// waiting. ESI's own advice is "don't operate at the limit"; the public service keeps a
	// quarter of the budget for bursts and for the ingest pollers that share the IP.
	defaultESIGuardPercent = 25
)

// upstreamToolRate resolves DATAAPI_UPSTREAM_TOOL_LIMIT (calls per minute per caller; 0
// disables the extra limit). Unset means defaultUpstreamToolLimit; garbage is an error.
func upstreamToolRate(getenv func(string) string) (dataapi.Rate, error) {
	n := defaultUpstreamToolLimit
	if v := strings.TrimSpace(getenv("DATAAPI_UPSTREAM_TOOL_LIMIT")); v != "" {
		var err error
		if n, err = strconv.Atoi(v); err != nil || n < 0 {
			return dataapi.Rate{}, fmt.Errorf("DATAAPI_UPSTREAM_TOOL_LIMIT: want a non-negative number of calls per minute, got %q", v)
		}
	}
	return dataapi.Rate{Max: n, Window: time.Minute}, nil
}

// esiGuardPercent resolves DATAAPI_ESI_GUARD_PERCENT (0-90).
func esiGuardPercent(getenv func(string) string) (int, error) {
	v := strings.TrimSpace(getenv("DATAAPI_ESI_GUARD_PERCENT"))
	if v == "" {
		return defaultESIGuardPercent, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 90 {
		return 0, fmt.Errorf("DATAAPI_ESI_GUARD_PERCENT: want 0-90, got %q", v)
	}
	return n, nil
}

// newToolsClient builds the tools client of the public service: an ESI client that never
// sleeps for a rate limit (the tool answers upstream_busy), keeps guard percent of every
// group's budget unspent and logs every call with esiObserver.
func newToolsClient(log *slog.Logger, guardPercent int) *tools.Client {
	return tools.NewClientWithESI(tools.PublicESIOptions{
		Observer:     esiObserver(log),
		FailFast:     true,
		GuardPercent: guardPercent,
	})
}

// esiObserver logs one structured line per ESI call: the route template, the rate-limit
// group, cache outcome, status and the budget ESI reports. No caller identity, no query
// string, no payload. A hit is a call served from the shared cache (nothing sent); a
// back-off is a call refused to protect the budget. Dashboards
// and alerts read these lines (msg "esi_call").
func esiObserver(log *slog.Logger) func(esi.CallEvent) {
	return func(ev esi.CallEvent) {
		level := slog.LevelInfo
		if ev.BackedOff || ev.Status >= http.StatusBadRequest {
			level = slog.LevelWarn
		}
		log.LogAttrs(context.Background(), level, "esi_call",
			slog.String("route", ev.Route),
			slog.String("group", ev.Group),
			slog.String("cache", ev.Cache),
			slog.Int("status", ev.Status),
			slog.Bool("sent", ev.Sent),
			slog.Bool("backed_off", ev.BackedOff),
			slog.String("reason", ev.Reason),
			slog.Int64("duration_ms", ev.Duration.Milliseconds()),
			slog.Int("rate_remaining", ev.RateRemaining),
			slog.Int("rate_limit", ev.RateLimit),
			slog.Int("error_limit_remain", ev.ErrorLimitRemain),
			slog.Int64("retry_after_s", int64(ev.RetryAfter/time.Second)),
		)
	}
}
