package version

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// setBuild swaps the link-time variable for the test and restores it afterwards.
func setBuild(t *testing.T, v string) {
	t.Helper()
	old := buildVersion
	buildVersion = v
	t.Cleanup(func() { buildVersion = old })
}

func TestVersion_Precedence(t *testing.T) {
	cases := []struct {
		name  string
		env   string
		build string
		want  string
	}{
		{"nothing set falls back to the baked-in release", "", "", fallback},
		{"ldflags value is used when the env is unset", "", "v1.2.3", "1.2.3"},
		{"env wins over ldflags", "v0.10.3", "v9.9.9", "0.10.3"},
		{"leading v is stripped from the env value", "v0.10.3", "", "0.10.3"},
		{"a sha deploy tag is kept as is", "sha-abc1234", "", "sha-abc1234"},
		{"an env that is only a v defers to ldflags", "v", "v1.2.3", "1.2.3"},
		{"a build that is only a v defers to the fallback", "", "v", fallback},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setBuild(t, tc.build)
			t.Setenv("API_VERSION", tc.env)
			require.Equal(t, tc.want, Version())
		})
	}
}

func TestRaw_KeepsTheTagVerbatim(t *testing.T) {
	cases := []struct {
		name  string
		env   string
		build string
		want  string
	}{
		{"nothing set is empty", "", "", ""},
		{"ldflags value is returned with its v", "", "v1.2.3", "v1.2.3"},
		{"env wins and keeps its v", "v0.10.3", "v9.9.9", "v0.10.3"},
		{"sha deploy tag", "sha-abc1234", "", "sha-abc1234"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setBuild(t, tc.build)
			t.Setenv("API_VERSION", tc.env)
			require.Equal(t, tc.want, Raw())
		})
	}
}

// TestLinkerFlagInjectsTheVersion pins the exact -X path the Dockerfiles and the
// release workflows use: if the package or variable is ever renamed, the linker
// silently ignores a stale -X and every release would report the fallback. It
// builds testdata/printversion for real with that flag.
func TestLinkerFlagInjectsTheVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary with go run")
	}
	const flag = "-X eve-cyno.dev/go/data/version.buildVersion="

	run := func(t *testing.T, ldflags string, env ...string) string {
		t.Helper()
		cmd := exec.Command("go", "run", "-ldflags", ldflags, "./testdata/printversion")
		cmd.Dir = "."
		cmd.Env = append(withoutEnv(os.Environ(), "API_VERSION"), env...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}

	t.Run("ldflags value is reported without its v", func(t *testing.T) {
		require.Equal(t, "version=test-1 raw=test-1", run(t, "-s -w "+flag+"test-1"))
		require.Equal(t, "version=0.10.3 raw=v0.10.3", run(t, "-s -w "+flag+"v0.10.3"))
	})
	t.Run("API_VERSION still beats ldflags", func(t *testing.T) {
		require.Equal(t, "version=0.11.0 raw=v0.11.0", run(t, flag+"v0.10.3", "API_VERSION=v0.11.0"))
	})
	t.Run("an empty ldflags value keeps the fallback", func(t *testing.T) {
		require.Equal(t, "version="+fallback+" raw=", run(t, flag))
	})
}

func withoutEnv(env []string, key string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}
