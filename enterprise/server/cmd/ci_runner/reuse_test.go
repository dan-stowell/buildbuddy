package main

import (
	"encoding/base64"
	"io"
	"io/fs"
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

func reuseTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			tree[rel] = "symlink:" + target
			return err
		}
		b, err := os.ReadFile(path)
		tree[rel] = string(b)
		return err
	}))
	return tree
}

func (f *reuseFixture) assertFresh(sha string) {
	f.t.Helper()
	fresh := f.t.TempDir()
	reuseGit(f.t, fresh, "clone", "--quiet", "--no-checkout", "file://"+f.source, ".")
	// A default clone does not include PR-only refs. Fetch the requested
	// object explicitly, just as the runner does.
	reuseGit(f.t, fresh, "fetch", "origin", sha)
	reuseGit(f.t, fresh, "checkout", "--quiet", sha)
	require.Equal(f.t, reuseTree(f.t, fresh), reuseTree(f.t, filepath.Join(f.root, repoDirName)))
}

func TestRunnerReuseOrdinaryCleanup(t *testing.T) {
	f := newReuseFixture(t)
	checkout := filepath.Join(f.root, repoDirName)
	reuseWrite(t, checkout, "tracked", "dirty\n")
	require.NoError(t, os.Remove(filepath.Join(checkout, "deleted")))
	reuseWrite(t, checkout, "generated/output", "stale")
	reuseWrite(t, checkout, "ignored/output", "stale")
	reuseGit(t, f.source, "checkout", "-b", "z")
	reuseWrite(t, f.source, "tracked", "requested\n")
	sha := reuseCommit(t, f.source)
	require.NoError(t, f.setup("z", "", sha))
	f.assertFresh(sha)
}

func TestRunnerReuseRefChanges(t *testing.T) {
	for _, kind := range []string{"force-push", "moving-tag", "pr-ref", "deleted-branch", "fetch-error"} {
		t.Run(kind, func(t *testing.T) {
			f := newReuseFixture(t)
			old := reuseGit(t, f.source, "rev-parse", "HEAD")
			reuseWrite(t, f.source, "tracked", "new\n")
			sha := reuseCommit(t, f.source)
			switch kind {
			case "force-push":
				require.NoError(t, f.setup("x", "", sha))
				reuseGit(t, f.source, "reset", "--hard", old)
				require.NoError(t, f.setup("x", "", ""))
				sha = old
			case "moving-tag":
				reuseGit(t, f.source, "tag", "release", old)
				require.NoError(t, f.setup("", "release", ""))
				reuseGit(t, f.source, "tag", "-f", "release", sha)
				require.NoError(t, f.setup("", "release", ""))
			case "pr-ref":
				reuseGit(t, f.source, "update-ref", "refs/pull/7/head", sha)
				reuseGit(t, f.source, "reset", "--hard", old)
				require.NoError(t, f.setup("", "", sha))
			case "deleted-branch":
				reuseGit(t, f.source, "checkout", "-b", "z")
				reuseGit(t, f.source, "branch", "-D", "x")
				require.Error(t, f.setup("x", "", ""))
				require.NoError(t, f.setup("z", "", sha))
			case "fetch-error":
				flags.Set(t, "pushed_repo_url", "file://"+filepath.Join(t.TempDir(), "missing"))
				require.Error(t, f.setup("x", "", sha))
				flags.Set(t, "pushed_repo_url", "file://"+f.source)
				require.NoError(t, f.setup("x", "", sha))
			}
			require.Equal(t, sha, reuseGit(t, filepath.Join(f.root, repoDirName), "rev-parse", "HEAD"))
			f.assertFresh(sha)
		})
	}
}

func TestRunnerReuseIndexLockRecovery(t *testing.T) {
	f := newReuseFixture(t)
	reuseWrite(t, filepath.Join(f.root, repoDirName), ".git/index.lock", "stale lock")
	sha := reuseGit(t, f.source, "rev-parse", "HEAD")
	require.NoError(t, f.setup("x", "", sha))
	f.assertFresh(sha)
}

func TestRunnerReuseCleanExclude(t *testing.T) {
	f := newReuseFixture(t)
	flags.Set(t, "git_clean_exclude", []string{"keep/"})
	checkout := filepath.Join(f.root, repoDirName)
	reuseWrite(t, checkout, "keep/cache", "preserved")
	reuseWrite(t, checkout, "discard", "removed")
	sha := reuseGit(t, f.source, "rev-parse", "HEAD")
	require.NoError(t, f.setup("x", "", sha))
	b, err := os.ReadFile(filepath.Join(checkout, "keep/cache"))
	require.NoError(t, err)
	require.Equal(t, "preserved", string(b))
	require.NoError(t, os.RemoveAll(filepath.Join(checkout, "keep")))
	f.assertFresh(sha)
}

func TestRunnerReuseShallowMerge(t *testing.T) {
	f := newReuseFixture(t)
	reuseGit(t, f.source, "checkout", "-b", "base")
	reuseWrite(t, f.source, "base-file", "base contents")
	base := reuseCommit(t, f.source)
	reuseGit(t, f.source, "checkout", "x")
	reuseWrite(t, f.source, "head-file", "head contents")
	head := reuseCommit(t, f.source)
	flags.Set(t, "target_branch", "base")
	flags.Set(t, "serialized_action", base64.StdEncoding.EncodeToString([]byte("name: test\ntriggers:\n  pull_request:\n    merge_with_base: true\n")))
	require.NoError(t, f.setup("x", "", head))
	require.NoError(t, f.ws.setupError)
	checkout := filepath.Join(f.root, repoDirName)
	require.Equal(t, "false", reuseGit(t, checkout, "rev-parse", "--is-shallow-repository"))
	require.Equal(t, head, reuseGit(t, checkout, "rev-parse", "HEAD^1"))
	require.Equal(t, base, reuseGit(t, checkout, "rev-parse", "HEAD^2"))
	// Compare Git trees: merge commit metadata is intentionally irrelevant.
	reuseGit(t, f.source, "-c", "user.name=Test", "-c", "user.email=test@example.com", "merge", "--no-edit", "base")
	wantTree := reuseGit(t, f.source, "rev-parse", "HEAD^{tree}")
	require.Equal(t, wantTree, reuseGit(t, checkout, "rev-parse", "HEAD^{tree}"))
	f.assertFresh(reuseGit(t, f.source, "rev-parse", "HEAD"))
}

// A workflow may clone a tool/dependency into its workspace. Without a clean
// exclusion, it must not become an input of the next unrelated invocation.
func TestRunnerReuseRemovesNestedRepository(t *testing.T) {
	f := newReuseFixture(t)
	checkout := filepath.Join(f.root, repoDirName)
	nested := filepath.Join(checkout, "old-dependency")
	require.NoError(t, os.Mkdir(nested, 0755))
	reuseGit(t, nested, "init")
	reuseGit(t, nested, "symbolic-ref", "HEAD", "refs/heads/main")
	reuseWrite(t, nested, "stale-input", "from previous invocation\n")
	reuseCommit(t, nested)
	reuseGit(t, f.source, "checkout", "-b", "z")
	reuseWrite(t, f.source, "tracked", "requested commit\n")
	sha := reuseCommit(t, f.source)
	require.NoError(t, f.setup("z", "", sha))
	require.Equal(t, sha, reuseGit(t, checkout, "rev-parse", "HEAD"))
	f.assertFresh(sha)
}

func TestRunnerReuseSubmodule(t *testing.T) {
	for _, scenario := range []string{"pointer-change", "removed"} {
		t.Run(scenario, func(t *testing.T) {
			f := newReuseFixture(t)
			sub := t.TempDir()
			reuseGit(t, sub, "init")
			reuseGit(t, sub, "symbolic-ref", "HEAD", "refs/heads/main")
			reuseWrite(t, sub, "input", "old submodule\n")
			reuseCommit(t, sub)
			reuseGit(t, f.source, "submodule", "add", "file://"+sub, "dependency")
			old := reuseCommit(t, f.source)
			require.NoError(t, f.setup("x", "", old))
			checkout := filepath.Join(f.root, repoDirName)
			// Model a workflow that initialized its dependencies on a prior run.
			reuseGit(t, checkout, "submodule", "update", "--init", "--recursive")
			if scenario == "pointer-change" {
				reuseWrite(t, sub, "input", "new submodule\n")
				next := reuseCommit(t, sub)
				reuseGit(t, filepath.Join(f.source, "dependency"), "fetch", "origin")
				reuseGit(t, filepath.Join(f.source, "dependency"), "checkout", next)
			} else {
				reuseGit(t, f.source, "rm", "-f", "dependency")
			}
			sha := reuseCommit(t, f.source)
			require.NoError(t, f.setup("x", "", sha))
			require.Equal(t, sha, reuseGit(t, checkout, "rev-parse", "HEAD"))
			fresh := t.TempDir()
			reuseGit(t, fresh, "clone", "--quiet", "file://"+f.source, ".")
			reuseGit(t, fresh, "checkout", "--quiet", sha)
			reuseGit(t, fresh, "submodule", "update", "--init", "--recursive")
			require.Equal(t, reuseTree(t, fresh), reuseTree(t, checkout))
		})
	}
}
