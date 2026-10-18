package tools_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/tools"
)

func TestForPublic_CopiesWithoutMutating(t *testing.T) {
	base := &tools.Deps{Client: tools.NewClient(), JaniceAPIKey: "project-key"}

	own := base.ForPublic("caller-key")
	require.Equal(t, "caller-key", own.JaniceAPIKey)
	require.Equal(t, "project-key", base.JaniceAPIKey, "the shared deps keep the project key")
	require.Same(t, base.Client, own.Client)

	require.Empty(t, base.ForPublic("").JaniceAPIKey, "no key: Janice stays unconfigured, never the project key")

	var nilDeps *tools.Deps
	require.Nil(t, nilDeps.ForPublic("x"))
}

func TestForPublic_ConcurrentCopies(t *testing.T) {
	base := &tools.Deps{Client: tools.NewClient(), JaniceAPIKey: "project-key"}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := string(rune('a' + i))
			require.Equal(t, key, base.ForPublic(key).JaniceAPIKey)
		}()
	}
	wg.Wait()
	require.Equal(t, "project-key", base.JaniceAPIKey)
}

func TestForPublic_SetsPublicAndKeyOnly(t *testing.T) {
	base := &tools.Deps{Client: tools.NewClient(), JaniceAPIKey: "project-key"}
	require.False(t, base.Public, "the zero value is the chat brain's text")
	own := base.ForPublic("caller-key")
	require.True(t, own.Public)
	require.Equal(t, "caller-key", own.JaniceAPIKey)
	require.False(t, base.Public, "the shared deps stay untouched")
	require.Same(t, base.Client, own.Client)
}
