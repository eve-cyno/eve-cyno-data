package dataapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The banner texts are the design's (data-account.html in the design system, one board per state); the numbers come
// from the store's limits. Codes the design has no board for (bad-form and the three delete refusals) keep the
// repo's text.
func TestAccountTexts_FollowTheDesign(t *testing.T) {
	notices := map[string]string{
		"cancelled":     "Sign-in cancelled. Nothing was changed. You can sign in again when you are ready.",
		"deleted":       "Your account is deleted. Its keys no longer work.",
		"session-ended": "Your session ended. Sign in again to go on.",
		"revoked":       "Key revoked. It stopped working at once.",
		"form-used":     "That form was already sent, so no second key was made. Your keys are listed below.",
		"rehomed": "This character changed hands, so it signed in to a new, empty account. " +
			"Keys made before the transfer stay with the previous owner.",
	}
	require.Len(t, accountNotices, len(notices))
	for code, want := range notices {
		require.Equal(t, want, accountNotices[code], code)
	}

	l := PortalLimits{MaxActive: 3, MaxCreatesPerDay: 10, KeyNameMax: 40}
	errs := map[string]string{
		"sign-in-failed":   "The sign-in did not complete. Start again with the button below.",
		"sign-in-token":    "EVE Online could not confirm the sign-in. Try again.",
		"sign-in-upstream": "EVE Online's login service did not answer. Try again in a minute.",
		"busy":             "Sign-in is busy right now. Try again in a minute.",
		"too-many-keys":    "You already have 3 active keys. Revoke one first.",
		"create-limit":     "You made 10 keys today. Try again after 00:00 UTC.",
		"key-name":         "That key name is not allowed. Use a shorter name without control characters.",
		"key-not-found":    "That key is not on this account, or it is already revoked.",
		"csrf":             "The page expired. Reload it and try again.",
		"cross-origin":     "That request came from another site, so it was refused.",
		"too-large":        "That request was too large.",
		"internal":         "Something went wrong on our side. Nothing was changed. Try again.",
		"blocked":          "This account is blocked: its keys do not work and no new key can be made. Ask on Discord.",
	}
	for code, want := range errs {
		got := errorOf(code, l)
		require.Equal(t, code, got.Code)
		require.Equal(t, want, got.Text, code)
	}
	other := PortalLimits{MaxActive: 5, MaxCreatesPerDay: 7}
	require.Equal(t, "You already have 5 active keys. Revoke one first.", errorOf("too-many-keys", other).Text)
	require.Equal(t, "You made 7 keys today. Try again after 00:00 UTC.", errorOf("create-limit", other).Text)
	require.Equal(t, "internal", errorOf("no-such-code", l).Code)
}
