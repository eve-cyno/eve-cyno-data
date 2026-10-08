package tools_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"eve-cyno.dev/go/data/tools"
)

func TestWithJaniceKey_CopiesWithoutMutating(t *testing.T) {
	base := &tools.Deps{Client: tools.NewClient(), JaniceAPIKey: "project-key"}

	own := base.WithJaniceKey("caller-key")
	require.Equal(t, "caller-key", own.JaniceAPIKey)
	require.Equal(t, "project-key", base.JaniceAPIKey, "the shared deps keep the project key")
	require.Same(t, base.Client, own.Client)

	require.Empty(t, base.WithJaniceKey("").JaniceAPIKey, "no key: Janice stays unconfigured, never the project key")

	var nilDeps *tools.Deps
	require.Nil(t, nilDeps.WithJaniceKey("x"))
}

func TestWithJaniceKey_ConcurrentCopies(t *testing.T) {
	base := &tools.Deps{Client: tools.NewClient(), JaniceAPIKey: "project-key"}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := string(rune('a' + i))
			require.Equal(t, key, base.WithJaniceKey(key).JaniceAPIKey)
		}()
	}
	wg.Wait()
	require.Equal(t, "project-key", base.JaniceAPIKey)
}
