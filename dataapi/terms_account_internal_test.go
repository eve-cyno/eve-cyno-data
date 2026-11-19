package dataapi

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The terms state the account page's cookie and session lifetimes and the delete window as the code sets them, so
// changing one of the constants fails here until the terms say the new value.
func TestTerms_AccountPageLifetimesFollowTheCode(t *testing.T) {
	hours := int(PortalSessionTTL / time.Hour)
	require.Equal(t, PortalSessionTTL, time.Duration(hours)*time.Hour, "the session life is whole hours")
	require.Equal(t, sessionMaxAge, int(PortalSessionTTL/time.Second), "the cookie lives as long as the session")
	require.Contains(t, termsMarkdown, fmt.Sprintf("a session cookie that lasts the %d hours of a sign-in", hours))
	require.Contains(t, termsMarkdown, fmt.Sprintf("it keeps a session of at most %d hours", hours))

	require.Zero(t, flowMaxAge%60, "the sign-in cookies live whole minutes")
	require.Contains(t, termsMarkdown, fmt.Sprintf("two cookies that live %d minutes while you sign in at EVE Online", flowMaxAge/60))

	require.Equal(t, 60, noticeMaxAge, "the terms say the notice cookie lives up to a minute")
	require.Contains(t, termsMarkdown, "which message to show, for up to a minute")

	require.Contains(t, termsMarkdown, fmt.Sprintf("which needs a sign-in from the last %d minutes", int(deleteWithin/time.Minute)))
}
