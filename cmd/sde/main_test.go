package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func env(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

func TestParseArgs_OutBeatsEnvBeatsDefault(t *testing.T) {
	o, err := parseArgs([]string{"-out", "/a/sde.sqlite"}, env(map[string]string{"EVE_CORE_SDE_PATH": "/b/sde.sqlite"}), io.Discard)
	require.NoError(t, err)
	require.Equal(t, "/a/sde.sqlite", o.out)
	require.False(t, o.force)

	o, err = parseArgs([]string{"-force"}, env(map[string]string{"EVE_CORE_SDE_PATH": "/b/sde.sqlite"}), io.Discard)
	require.NoError(t, err)
	require.Equal(t, "/b/sde.sqlite", o.out)
	require.True(t, o.force)

	t.Setenv("EVE_CORE_SDE_PATH", "")
	o, err = parseArgs(nil, env(nil), io.Discard)
	require.NoError(t, err)
	require.Equal(t, filepath.Join("data", "sde", "sde.sqlite"), o.out[len(o.out)-len(filepath.Join("data", "sde", "sde.sqlite")):])
}

func TestParseArgs_RejectsStrayArguments(t *testing.T) {
	_, err := parseArgs([]string{"extra"}, env(nil), io.Discard)
	require.Error(t, err)
}

type fakeRefresher struct {
	gotRecorded string
	latest      string
	rebuilt     bool
	err         error
}

func (f *fakeRefresher) Refresh(_ context.Context, recorded string) (string, bool, error) {
	f.gotRecorded = recorded
	return f.latest, f.rebuilt, f.err
}

func TestRun_RecordsTheBuildAndSkipsTheNextRun(t *testing.T) {
	out := filepath.Join(t.TempDir(), "sub", "sde.sqlite")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	f := &fakeRefresher{latest: "123_20260101", rebuilt: true}
	require.NoError(t, run(context.Background(), f, options{out: out}, log))
	require.Empty(t, f.gotRecorded)
	require.Equal(t, "123_20260101", readBuildID(out))

	f2 := &fakeRefresher{latest: "123_20260101"}
	require.NoError(t, run(context.Background(), f2, options{out: out}, log))
	require.Equal(t, "123_20260101", f2.gotRecorded, "the second run hands the recorded build back")

	f3 := &fakeRefresher{latest: "123_20260101", rebuilt: true}
	require.NoError(t, run(context.Background(), f3, options{out: out, force: true}, log))
	require.Empty(t, f3.gotRecorded, "-force ignores the recorded build")
}

func TestRun_FailureKeepsTheRecordedBuildAndReturnsTheError(t *testing.T) {
	out := filepath.Join(t.TempDir(), "sde.sqlite")
	require.NoError(t, os.WriteFile(buildIDPath(out), []byte("old\n"), 0o644))
	boom := errors.New("fuzzwork down")
	err := run(context.Background(), &fakeRefresher{err: boom}, options{out: out}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.ErrorIs(t, err, boom)
	require.Equal(t, "old", readBuildID(out))
}
