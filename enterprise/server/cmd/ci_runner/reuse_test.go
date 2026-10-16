package main

import (
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/require"
)

// These tests intentionally run serially: the real runner uses process-wide cwd
// and flags. Every remote is a temporary local repository; no Git server is used.
type reuseFixture struct {
	t            *testing.T
	source, root string
	ws           *workspace
}

func reuseGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(t.Context(), io.Discard, append([]string{"-C", dir}, args...)...)
	if err != nil {
		t.Fatalf("git %v: %s", args, err.Output)
	}
	return strings.TrimSpace(out)
}

func reuseWrite(t *testing.T, dir, name, contents string) {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, []byte(contents), 0644))
}

func reuseCommit(t *testing.T, dir string) string {
	t.Helper()
	reuseGit(t, dir, "add", "-A")
	reuseGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "fixture")
	return reuseGit(t, dir, "rev-parse", "HEAD")
}

func newReuseFixture(t *testing.T) *reuseFixture {
	t.Helper()
	t.Setenv("USE_SYSTEM_GIT_CREDENTIALS", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_AUTHOR_DATE", "2024-01-01T00:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2024-01-01T00:00:00Z")
	source, root := t.TempDir(), t.TempDir()
	reuseGit(t, source, "init")
	reuseGit(t, source, "symbolic-ref", "HEAD", "refs/heads/x")
	reuseWrite(t, source, "tracked", "original\n")
	reuseWrite(t, source, "deleted", "restore me\n")
	reuseWrite(t, source, ".gitignore", "ignored/\n")
	reuseCommit(t, source)
	flags.Set(t, "pushed_repo_url", "file://"+source)
	flags.Set(t, "target_repo_url", "file://"+source)
	flags.Set(t, "target_branch", "")
	flags.Set(t, "pushed_branch", "x")
	flags.Set(t, "pushed_tag", "")
	flags.Set(t, "commit_sha", "")
	flags.Set(t, "git_fetch_filters", []string{})
	flags.Set(t, "git_fetch_depth", 1)
	flags.Set(t, "git_clean_exclude", []string{})
	flags.Set(t, "patch_uri", []string{})
	flags.Set(t, "serialized_action", base64.StdEncoding.EncodeToString([]byte("name: test\n")))
	flags.Set(t, "fallback_to_clean_checkout", true)
	log := newInvocationLog(nil)
	log.writer = io.Discard
	f := &reuseFixture{t: t, source: source, root: root, ws: &workspace{rootDir: root, startTime: time.Now(), log: &buildEventReporter{log: log}}}
	t.Chdir(root)
	require.NoError(t, f.ws.setup(t.Context()))
	return f
}

func (f *reuseFixture) setup(branch, tag, sha string) error {
	f.t.Helper()
	flags.Set(f.t, "pushed_branch", branch)
	flags.Set(f.t, "pushed_tag", tag)
	flags.Set(f.t, "commit_sha", sha)
	require.NoError(f.t, os.Chdir(f.root))
	// A recycled invocation constructs a new workspace, keeping the on-disk repo.
	f.ws = &workspace{rootDir: f.root, startTime: time.Now(), log: f.ws.log}
	return f.ws.setup(f.t.Context())
}

// Deinitialization must keep the object store, so initializing again can work
// without contacting the original submodule remote. Disable fallback so a fresh
// clone cannot hide an unsuccessful recycled-workspace sync.
func TestRunnerReuseSubmoduleObjectsSurviveOffline(t *testing.T) {
	f := newReuseFixture(t)
	flags.Set(t, "fallback_to_clean_checkout", false)
	sub := t.TempDir()
	reuseGit(t, sub, "init")
	reuseWrite(t, sub, "input", "cached object\n")
	object := reuseCommit(t, sub)
	reuseGit(t, f.source, "submodule", "add", "file://"+sub, "dependency")
	sha := reuseCommit(t, f.source)
	require.NoError(t, f.setup("x", "", sha))
	checkout := filepath.Join(f.root, repoDirName)
	reuseGit(t, checkout, "submodule", "update", "--init")
	require.NoError(t, os.RemoveAll(sub))
	require.NoError(t, f.setup("x", "", sha))
	_, err := os.Stat(filepath.Join(checkout, "dependency/input"))
	require.True(t, os.IsNotExist(err), "recycled submodule must be uninitialized: %v", err)
	modules := filepath.Join(checkout, ".git/modules/dependency")
	require.Equal(t, "blob", reuseGit(t, modules, "cat-file", "-t", object+":input"))
	reuseGit(t, checkout, "submodule", "update", "--init")
	contents, err := os.ReadFile(filepath.Join(checkout, "dependency/input"))
	require.NoError(t, err)
	require.Equal(t, "cached object\n", string(contents))
}
