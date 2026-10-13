package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/buildbuddy-io/buildbuddy/cli/testutil/testcli"
	spawn_diff "github.com/buildbuddy-io/buildbuddy/proto/spawn_diff"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Set via x_defs in the BUILD file.
var (
	oldCompactLogRunfilePath string
	newCompactLogRunfilePath string
)

func runfilePath(t *testing.T, rlocationPath string) string {
	t.Helper()
	p, err := runfiles.Rlocation(rlocationPath)
	require.NoError(t, err, "resolve runfile %q", rlocationPath)
	return p
}

// Keep each case independent and exercise explain without a Bazel or Git workspace.
func fixtureLogs(t *testing.T) (dir, old, new string) {
	t.Helper()
	dir = t.TempDir()
	copyLog := func(rlocationPath, name string) string {
		data, err := os.ReadFile(runfilePath(t, rlocationPath))
		require.NoError(t, err)
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, data, 0600))
		return p
	}
	return dir, copyLog(oldCompactLogRunfilePath, "old.binpb.zst"), copyLog(newCompactLogRunfilePath, "new.binpb.zst")
}

// The two compact execution logs are for builds of the same two genrule
// targets but with different output contents, so we expect bb explain to
// report both spawns as modified.
func assertExpectedDiff(t *testing.T, result *spawn_diff.DiffResult) {
	require.Len(t, result.GetSpawnDiffs(), 2)
	var labels []string
	for _, sd := range result.GetSpawnDiffs() {
		labels = append(labels, sd.GetTargetLabel())
		assert.Equal(t, "Genrule", sd.GetMnemonic())
		assert.NotNil(t, sd.GetModified(), "expected spawn %q to be modified", sd.GetTargetLabel())
		assert.NotEmpty(t, sd.GetModified().GetDiffs(), "expected spawn %q to have at least one diff", sd.GetTargetLabel())
	}
	assert.ElementsMatch(t, []string{
		"//server/version:populate_commit",
		"//server/version:populate_version",
	}, labels)
}

func TestExplainJsonOutputIsClean(t *testing.T) {
	dir, old, new := fixtureLogs(t)

	stdout, stderr, err := testcli.SplitOutput(
		testcli.Command(t, dir, "explain",
			"--old="+old,
			"--new="+new,
			"--output_format=json",
		),
	)
	require.NoError(t, err, "bb explain failed; stderr: %s", stderr)

	var result spawn_diff.DiffResult
	require.NoError(t, protojson.Unmarshal(stdout, &result),
		"stdout should be valid protojson with no extra output from the CLI stack; stdout: %q", stdout)
	assertExpectedDiff(t, &result)
}

func TestExplainProtoOutputIsClean(t *testing.T) {
	dir, old, new := fixtureLogs(t)

	stdout, stderr, err := testcli.SplitOutput(
		testcli.Command(t, dir, "explain",
			"--old="+old,
			"--new="+new,
			"--output_format=proto",
		),
	)
	require.NoError(t, err, "bb explain failed; stderr: %s", stderr)

	var result spawn_diff.DiffResult
	require.NoError(t, proto.Unmarshal(stdout, &result),
		"stdout should be valid proto binary with no extra output from the CLI stack; stdout len: %d", len(stdout))
	assertExpectedDiff(t, &result)
}
