package config_test

import (
	"bytes"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/workflow/config"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/workflow/config/test_data"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rnpb "github.com/buildbuddy-io/buildbuddy/proto/runner"
)

func TestWorkflowConf_Parse_BasicConfig_Valid(t *testing.T) {
	conf, err := config.NewConfig(bytes.NewReader(test_data.BasicYaml))

	assert.NoError(t, err)
	assert.Equal(t, &config.BuildBuddyConfig{
		Actions: []*config.Action{
			{
				Name: "Build and test",
				Triggers: &config.Triggers{
					Push: &config.PushTrigger{
						Branches: []string{"main"},
					},
					PullRequest: &config.PullRequestTrigger{
						Branches: []string{"main"},
					},
				},
				Steps: []*rnpb.Step{
					{
						Run: "bazel build //...",
					},
					{
						Run: "bazel test //...",
					},
				},
			},
		},
	}, conf)
}

func TestWorkflowConf_Parse_YamlWithRunBlock(t *testing.T) {
	conf, err := config.NewConfig(bytes.NewReader(test_data.YamlWithRunBlock))

	assert.NoError(t, err)
	assert.Equal(t, &config.BuildBuddyConfig{
		Actions: []*config.Action{
			{
				Name: "Build and test",
				Triggers: &config.Triggers{
					Push: &config.PushTrigger{
						Branches: []string{"main"},
					},
				},
				Steps: []*rnpb.Step{
					{
						Run: `echo "This is a multi-line run block"
echo "Should still parse!"
`,
					},
				},
			},
		},
	}, conf)
}

func TestWorkflowConf_Parse_InvalidConfig_Error(t *testing.T) {
	// Unquoted bazel command
	s := `
actions:
  - name: Test
    steps:
      - "bazel test foo
`
	conf, err := config.NewConfig(strings.NewReader(s))
	assert.Nil(t, conf)
	assert.Error(t, err)
}

func TestMatchesAnyTrigger_SupportsBasicWildcard(t *testing.T) {
	for _, testCase := range []struct {
		pattern, branchName string
		shouldMatch         bool
	}{
		{"main", "main", true},
		{"main", "other", false},
		{"main", "gh-readonly-queue/main/pr-1-1111111111111111111111111111111111111111", false},
		{"*", "main", true},
		{"*", "other", true},
		{"*", "gh-readonly-queue/main/pr-1-1111111111111111111111111111111111111111", true},
		{"gh-readonly-queue/*", "main", false},
		{"gh-readonly-queue/*", "other", false},
		{"gh-readonly-queue/*", "gh-readonly-queue", false},
		{"gh-readonly-queue/*", "gh-readonly-queue/", true},
		{"gh-readonly-queue/*", "gh-readonly-queue/main/pr-1-1111111111111111111111111111111111111111", true},
		{"gh-readonly-queue/*", "gh-READONLY-queue/main/pr-1-1111111111111111111111111111111111111111", false},
		{"release-*", "release-20240101", true},
		{"release-*", "releasefoo", false},
		{"*-release", "20240101-release", true},
		{"*-release", "foorelease", false},
		{"prefix*suffix", "prefix_suffix", true},
		{"prefix*suffix", "_prefix_suffix", false},
		{"prefix*suffix", "prefix_suffix_", false},
		{"prefix*suffix", "prefixsuffix", true},
	} {
		action := &config.Action{
			Triggers: &config.Triggers{
				Push: &config.PushTrigger{Branches: []string{testCase.pattern}},
			},
		}
		event := "push"

		match := config.MatchesAnyTrigger(action, event, testCase.branchName, "" /*=tag*/, "", false)

		assert.Equal(t, testCase.shouldMatch, match, "expected match(%q, %q) => %v", testCase.branchName, testCase.pattern, testCase.shouldMatch)
	}
}

func TestMatchesAndTrigger_NegationPatterns(t *testing.T) {
	for _, tc := range []struct {
		name           string
		patterns       []string
		branch         string
		shouldMatch    []string
		shouldNotMatch []string
	}{
		{
			name:           "negation pattern with exact branch",
			patterns:       []string{"*", "!main"},
			shouldMatch:    []string{"feature", "main1"},
			shouldNotMatch: []string{"main"},
		},
		{
			name:           "negation pattern with wildcard",
			patterns:       []string{"*", "!release-*"},
			shouldMatch:    []string{"main", "feature", "releasefoo"},
			shouldNotMatch: []string{"release-20240101"},
		},
		{
			name:           "negation pattern with exception - last match wins",
			patterns:       []string{"*", "!release-*", "release-exception", "some-other-branch"},
			shouldMatch:    []string{"main", "feature", "release-exception", "some-other-branch"},
			shouldNotMatch: []string{"release-20240101"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action := &config.Action{
				Triggers: &config.Triggers{
					Push: &config.PushTrigger{Branches: tc.patterns},
				},
			}
			event := "push"
			for _, branch := range tc.shouldMatch {
				m := config.MatchesAnyTrigger(action, event, branch, "", "", false)
				assert.True(t, m, "MatchesAnyTrigger(%v, %q) should be true", branch, tc.patterns)
			}
			for _, branch := range tc.shouldNotMatch {
				m := config.MatchesAnyTrigger(action, event, branch, "", "", false)
				assert.False(t, m, "MatchesAnyTrigger(%v, %q) should be false", branch, tc.patterns)
			}
		})
	}
}

func TestMatchesAnyTrigger_TagPushMatchesTagPattern(t *testing.T) {
	for _, tc := range []struct {
		pattern, tag string
		shouldMatch  bool
	}{
		{"v*", "v1.0.0", true},
		{"v*", "v2.0", true},
		{"v*", "release-1.0", false},
		{"*", "v1.0.0", true},
		{"*", "anything", true},
		{"v1.0.0", "v1.0.0", true},
		{"v1.0.0", "v1.0.1", false},
		{"release-*", "release-2024", true},
		{"release-*", "v1.0.0", false},
	} {
		action := &config.Action{
			Triggers: &config.Triggers{
				Push: &config.PushTrigger{Tags: []string{tc.pattern}},
			},
		}
		match := config.MatchesAnyTrigger(action, "push", "", tc.tag, "", false)
		assert.Equal(t, tc.shouldMatch, match, "expected match(tag=%q, pattern=%q) => %v", tc.tag, tc.pattern, tc.shouldMatch)
	}
}

func TestMatchesAnyTrigger_TagPushDoesNotMatchBranchOnlyTrigger(t *testing.T) {
	action := &config.Action{
		Triggers: &config.Triggers{
			Push: &config.PushTrigger{Branches: []string{"*"}},
		},
	}
	match := config.MatchesAnyTrigger(action, "push", "", "v1.0.0", "", false)
	assert.False(t, match, "tag push should not match branch-only trigger")
}

func TestMatchesAnyTrigger_BranchPushDoesNotMatchTagOnlyTrigger(t *testing.T) {
	action := &config.Action{
		Triggers: &config.Triggers{
			Push: &config.PushTrigger{Tags: []string{"v*"}},
		},
	}
	match := config.MatchesAnyTrigger(action, "push", "main", "", "", false)
	assert.False(t, match, "branch push should not match tag-only trigger")
}

func TestMatchesAnyTrigger_PullRequestTypes(t *testing.T) {
	// An action scoped to "ready_for_review" runs only on that pull_request
	// action, not on the default push-like actions.
	readyForReviewOnly := &config.Action{
		Triggers: &config.Triggers{
			PullRequest: &config.PullRequestTrigger{Branches: []string{"*"}, Types: []string{"ready_for_review"}},
		},
	}
	assert.True(t, config.MatchesAnyTrigger(readyForReviewOnly, "pull_request", "main", "", "ready_for_review", false))
	assert.False(t, config.MatchesAnyTrigger(readyForReviewOnly, "pull_request", "main", "", "opened", false))
	assert.False(t, config.MatchesAnyTrigger(readyForReviewOnly, "pull_request", "main", "", "synchronize", false))

	// A default pull_request trigger (no types) runs on the default actions but
	// not on "ready_for_review".
	defaultPR := &config.Action{
		Triggers: &config.Triggers{
			PullRequest: &config.PullRequestTrigger{Branches: []string{"*"}},
		},
	}
	assert.True(t, config.MatchesAnyTrigger(defaultPR, "pull_request", "main", "", "opened", false))
	assert.True(t, config.MatchesAnyTrigger(defaultPR, "pull_request", "main", "", "synchronize", false))
	assert.False(t, config.MatchesAnyTrigger(defaultPR, "pull_request", "main", "", "ready_for_review", false))

	// An empty action (e.g. a pull_request_review approval re-run) matches a
	// default trigger but not a types-scoped one.
	assert.True(t, config.MatchesAnyTrigger(defaultPR, "pull_request", "main", "", "", false))
	assert.False(t, config.MatchesAnyTrigger(readyForReviewOnly, "pull_request", "main", "", "", false))

	// Base-branch patterns are still honored alongside the type filter.
	scoped := &config.Action{
		Triggers: &config.Triggers{
			PullRequest: &config.PullRequestTrigger{Branches: []string{"main"}, Types: []string{"ready_for_review"}},
		},
	}
	assert.True(t, config.MatchesAnyTrigger(scoped, "pull_request", "main", "", "ready_for_review", false))
	assert.False(t, config.MatchesAnyTrigger(scoped, "pull_request", "feature", "", "ready_for_review", false))
}

func TestMatchesAnyTrigger_PullRequestApproved(t *testing.T) {
	// By default (no types specified), the pull request trigger fires on "approved" actions.
	defaultPR := &config.Action{
		Triggers: &config.Triggers{
			PullRequest: &config.PullRequestTrigger{Branches: []string{"*"}},
		},
	}
	assert.True(t, config.MatchesAnyTrigger(defaultPR, "pull_request", "main", "", "approved", false))

	// A trigger can opt in to running only on approval via `types: [ approved ]`.
	approvedOnly := &config.Action{
		Triggers: &config.Triggers{
			PullRequest: &config.PullRequestTrigger{Branches: []string{"*"}, Types: []string{"approved"}},
		},
	}
	assert.True(t, config.MatchesAnyTrigger(approvedOnly, "pull_request", "main", "", "approved", false))
	assert.False(t, config.MatchesAnyTrigger(approvedOnly, "pull_request", "main", "", "opened", false))
	assert.False(t, config.MatchesAnyTrigger(approvedOnly, "pull_request", "main", "", "synchronize", false))
}

func TestMatchesAnyTrigger_PullRequestAutoMergeEnabled(t *testing.T) {
	// By default (no types specified), the pull request trigger should not fire on "auto_merge_enabled" actions.
	defaultPR := &config.Action{
		Triggers: &config.Triggers{
			PullRequest: &config.PullRequestTrigger{Branches: []string{"*"}},
		},
	}
	assert.False(t, config.MatchesAnyTrigger(defaultPR, "pull_request", "main", "", "auto_merge_enabled", false))

	// If explicitly requested, the trigger should fire.
	autoMergeOnly := &config.Action{
		Triggers: &config.Triggers{
			PullRequest: &config.PullRequestTrigger{Branches: []string{"*"}, Types: []string{"auto_merge_enabled"}},
		},
	}
	assert.True(t, config.MatchesAnyTrigger(autoMergeOnly, "pull_request", "main", "", "auto_merge_enabled", false))
	assert.False(t, config.MatchesAnyTrigger(autoMergeOnly, "pull_request", "main", "", "opened", false))
	assert.False(t, config.MatchesAnyTrigger(autoMergeOnly, "pull_request", "main", "", "approved", false))
}

func TestMatchesAnyTrigger_PullRequestSkipDrafts(t *testing.T) {
	// By default, drafts trigger the workflow.
	defaultPR := &config.Action{
		Triggers: &config.Triggers{
			PullRequest: &config.PullRequestTrigger{Branches: []string{"*"}},
		},
	}
	assert.True(t, config.MatchesAnyTrigger(defaultPR, "pull_request", "main", "", "opened", true /*=isDraft*/))

	// With skip_drafts, drafts don't trigger the workflow, but non-drafts do.
	skipDrafts := &config.Action{
		Triggers: &config.Triggers{
			PullRequest: &config.PullRequestTrigger{Branches: []string{"*"}, SkipDrafts: true},
		},
	}
	assert.False(t, config.MatchesAnyTrigger(skipDrafts, "pull_request", "main", "", "opened", true /*=isDraft*/))
	assert.False(t, config.MatchesAnyTrigger(skipDrafts, "pull_request", "main", "", "synchronize", true /*=isDraft*/))
	assert.True(t, config.MatchesAnyTrigger(skipDrafts, "pull_request", "main", "", "opened", false /*=isDraft*/))
	assert.True(t, config.MatchesAnyTrigger(skipDrafts, "pull_request", "main", "", "synchronize", false /*=isDraft*/))
	// Marking a draft ready for review triggers the workflow by default.
	assert.True(t, config.MatchesAnyTrigger(skipDrafts, "pull_request", "main", "", "ready_for_review", false /*=isDraft*/))

	// If types are set explicitly, ready_for_review is not added.
	skipDraftsWithTypes := &config.Action{
		Triggers: &config.Triggers{
			PullRequest: &config.PullRequestTrigger{Branches: []string{"*"}, SkipDrafts: true, Types: []string{"synchronize"}},
		},
	}
	assert.False(t, config.MatchesAnyTrigger(skipDraftsWithTypes, "pull_request", "main", "", "ready_for_review", false /*=isDraft*/))
	assert.False(t, config.MatchesAnyTrigger(skipDraftsWithTypes, "pull_request", "main", "", "synchronize", true /*=isDraft*/))
	assert.True(t, config.MatchesAnyTrigger(skipDraftsWithTypes, "pull_request", "main", "", "synchronize", false /*=isDraft*/))

	// Manual dispatches always run, even for drafts.
	assert.True(t, config.MatchesAnyTrigger(skipDrafts, "manual_dispatch", "main", "", "", true /*=isDraft*/))
}

func TestMatchesAnyTrigger_TagNegationPatterns(t *testing.T) {
	action := &config.Action{
		Triggers: &config.Triggers{
			Push: &config.PushTrigger{Tags: []string{"v*", "!v0.9.0"}},
		},
	}
	assert.True(t, config.MatchesAnyTrigger(action, "push", "", "v1.0.0", "", false))
	assert.False(t, config.MatchesAnyTrigger(action, "push", "", "v0.9.0", "", false))
}

func TestMatchesAnyTrigger_BothBranchAndTagTriggers(t *testing.T) {
	action := &config.Action{
		Triggers: &config.Triggers{
			Push: &config.PushTrigger{
				Branches: []string{"main", "release-*"},
				Tags:     []string{"v*"},
			},
		},
	}

	// Tag push matches tag pattern, not branch pattern.
	assert.True(t, config.MatchesAnyTrigger(action, "push", "", "v1.0.0", "", false))
	assert.False(t, config.MatchesAnyTrigger(action, "push", "", "nightly-2024", "", false))

	// Branch push matches branch pattern, not tag pattern.
	assert.True(t, config.MatchesAnyTrigger(action, "push", "main", "", "", false))
	assert.True(t, config.MatchesAnyTrigger(action, "push", "release-2024", "", "", false))
	assert.False(t, config.MatchesAnyTrigger(action, "push", "feature-x", "", "", false))

	// Tag named "main" matches tag patterns (not branch patterns),
	// so it should not match since "main" doesn't match "v*".
	assert.False(t, config.MatchesAnyTrigger(action, "push", "", "main", "", false))
}

func TestAllowsConcurrentRunsOnBranch(t *testing.T) {
	for _, tc := range []struct {
		name          string
		patterns      []string
		branch        string
		defaultBranch string
		want          bool
	}{
		// When unset, allows concurrent runs on the default branch.
		{name: "unset allows default branch", patterns: nil, branch: "main", defaultBranch: "main", want: true},
		{name: "unset does not allow other branches", patterns: nil, branch: "feature", defaultBranch: "main", want: false},
		{name: "unset with unknown default branch allows everything", patterns: nil, branch: "feature", defaultBranch: "", want: true},
		{name: "empty list blocks default branch", patterns: []string{}, branch: "main", defaultBranch: "main", want: false},
		{name: "empty list blocks other branches", patterns: []string{}, branch: "feature", defaultBranch: "main", want: false},
		{name: "explicit list omitting default branch", patterns: []string{"staging"}, branch: "main", defaultBranch: "main", want: false},
		{name: "exact match", patterns: []string{"staging"}, branch: "staging", defaultBranch: "main", want: true},
		{name: "exact non-match", patterns: []string{"staging"}, branch: "feature", defaultBranch: "main", want: false},
		{name: "wildcard match", patterns: []string{"release-*"}, branch: "release-20240101", defaultBranch: "main", want: true},
		{name: "wildcard non-match", patterns: []string{"release-*"}, branch: "releasefoo", defaultBranch: "main", want: false},
		{name: "multiple patterns matches second", patterns: []string{"staging", "release-*"}, branch: "release-1", defaultBranch: "main", want: true},
		{name: "multiple patterns no match", patterns: []string{"staging", "release-*"}, branch: "feature", defaultBranch: "main", want: false},
		{name: "negation last match wins", patterns: []string{"release-*", "!release-exception"}, branch: "release-exception", defaultBranch: "main", want: false},
		{name: "negation does not affect other branches", patterns: []string{"release-*", "!release-exception"}, branch: "release-1", defaultBranch: "main", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action := &config.Action{AllowConcurrentRunsOnBranches: tc.patterns}
			assert.Equal(t, tc.want, action.AllowsConcurrentRunsOnBranch(tc.branch, tc.defaultBranch),
				"AllowsConcurrentRunsOnBranch(%q, %q) with patterns %v", tc.branch, tc.defaultBranch, tc.patterns)
		})
	}
}

func TestGetGitFetchFilters(t *testing.T) {
	for _, test := range []struct {
		Name    string
		YAML    string
		Filters []string
	}{
		{
			Name:    "Default",
			YAML:    "",
			Filters: []string{"blob:none"},
		},
		{
			Name:    "EmptyList",
			YAML:    "git_fetch_filters: []",
			Filters: []string{},
		},
		{
			Name:    "NonEmptyList",
			YAML:    `git_fetch_filters: ["tree:0"]`,
			Filters: []string{"tree:0"},
		},
	} {
		t.Run(test.Name, func(t *testing.T) {
			s := `actions: [ { name: Test, ` + test.YAML + ` } ]`
			cfg, err := config.NewConfig(strings.NewReader(s))
			require.NoError(t, err)
			require.Equal(t, test.Filters, cfg.Actions[0].GetGitFetchFilters())
		})
	}
}

func TestCodeSearchAction(t *testing.T) {
	apiURL, err := url.Parse("grpcs://example.com")
	require.NoError(t, err)
	ghURL := "https://github.com/buildbuddy-io/buildbuddy"

	action := config.CodesearchIncrementalUpdateAction(apiURL, ghURL, "master")

	require.NotNil(t, action)
	assert.Equal(t, config.CSIncrementalUpdateName, action.Name)
	require.NotNil(t, action.Triggers)
	require.NotNil(t, action.Triggers.Push)
	assert.Equal(t, []string{"master"}, action.Triggers.Push.Branches)
	require.NotNil(t, action.Steps)
	assert.Len(t, action.Steps, 1)
	assert.Contains(t, action.Steps[0].Run, apiURL.String())
	assert.Contains(t, action.Steps[0].Run, ghURL)
}

func TestGetMergeWithBaseInterval(t *testing.T) {
	dur := func(d time.Duration) *time.Duration { return &d }
	for _, test := range []struct {
		name     string
		interval *time.Duration
		want     *time.Duration
		wantErr  bool
	}{
		{name: "unset returns nil", interval: nil, want: nil},
		{name: "valid interval", interval: dur(2 * time.Hour), want: dur(2 * time.Hour)},
		{name: "max interval allowed", interval: dur(3 * time.Hour), want: dur(3 * time.Hour)},
		{name: "too large rejected", interval: dur(4 * time.Hour), wantErr: true},
		{name: "zero rejected", interval: dur(0), wantErr: true},
		{name: "negative rejected", interval: dur(-1 * time.Hour), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			trigger := &config.PullRequestTrigger{MergeWithBaseInterval: test.interval}
			got, err := trigger.GetMergeWithBaseInterval()
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if test.want == nil {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, *test.want, *got)
		})
	}
}

// TestBughuntTriggerMatrix checks action sets, including the documented push-based
// merge queue integration. The branch argument for PR events is the base branch.
func TestBughuntTriggerMatrix(t *testing.T) {
	cfg, err := config.NewConfig(strings.NewReader(`
actions:
  - name: push
    triggers: {push: {branches: ['*', '!release/*', 'release/stable']}}
  - name: pr
    triggers: {pull_request: {branches: [main], skip_drafts: true}}
  - name: queue
    triggers: {push: {branches: ['gh-readonly-queue/main/*']}}
  - name: release
    triggers: {push: {tags: ['v*']}}
  - name: manual
`))
	require.NoError(t, err)
	for _, tc := range []struct {
		name, event, branch, tag, action string
		draft                            bool
		want                             []string
	}{
		{name: "unicode push", event: "push", branch: "feature/日本語", want: []string{"push"}},
		{name: "excluded push", event: "push", branch: "release/next"},
		{name: "reincluded push", event: "push", branch: "release/stable", want: []string{"push"}},
		{name: "queue push", event: "push", branch: "gh-readonly-queue/main/pr-42", want: []string{"push", "queue"}},
		{name: "tag", event: "push", tag: "v1.2", want: []string{"release"}},
		{name: "PR base", event: "pull_request", branch: "main", action: "synchronize", want: []string{"pr"}},
		{name: "PR wrong base", event: "pull_request", branch: "feature/main", action: "opened"},
		{name: "draft", event: "pull_request", branch: "main", action: "opened", draft: true},
		{name: "ready", event: "pull_request", branch: "main", action: "ready_for_review", want: []string{"pr"}},
		{name: "labels only", event: "pull_request", branch: "main", action: "labeled"},
		{name: "unrelated", event: "issues", branch: "main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, a := range cfg.Actions {
				if config.MatchesAnyTrigger(a, tc.event, tc.branch, tc.tag, tc.action, tc.draft) {
					got = append(got, a.Name)
				}
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestBughuntConfigValues(t *testing.T) {
	cfg, err := config.NewConfig(strings.NewReader(`
actions:
 - name: defaults
 - name: explicit
   git_fetch_depth: 17
   git_fetch_filters: []
   timeout: 30m
   platform_properties: {OSFamily: Linux, custom: value}
   triggers: {pull_request: {merge_with_base: false}}
`))
	require.NoError(t, err)
	require.Len(t, cfg.Actions, 2)
	a, b := cfg.Actions[0], cfg.Actions[1]
	assert.Nil(t, a.GitFetchDepth)
	assert.Nil(t, a.Timeout)
	assert.Equal(t, []string{"blob:none"}, a.GetGitFetchFilters())
	assert.True(t, a.GetTriggers().GetPullRequestTrigger().GetMergeWithBase())
	require.NotNil(t, b.GitFetchDepth)
	assert.Equal(t, 17, *b.GitFetchDepth)
	require.NotNil(t, b.Timeout)
	assert.Equal(t, 30*time.Minute, *b.Timeout)
	assert.Empty(t, b.GetGitFetchFilters())
	assert.Equal(t, map[string]string{"OSFamily": "Linux", "custom": "value"}, b.PlatformProperties)
	assert.False(t, b.GetTriggers().GetPullRequestTrigger().GetMergeWithBase())
	for _, input := range []string{"actions: [", "actions: [{timeout: someday}]", "actions: [{git_fetch_depth: nope}]"} {
		_, err := config.NewConfig(strings.NewReader(input))
		assert.Error(t, err, "input: %s", input)
	}
}

func TestBughuntGlobDoesNotOverlap(t *testing.T) {
	// A '*' can match zero or more characters, but cannot consume a negative
	// number of characters by overlapping the literal prefix and suffix.
	a := &config.Action{Triggers: &config.Triggers{Push: &config.PushTrigger{Branches: []string{"ab*bc"}}}}
	assert.False(t, config.MatchesAnyTrigger(a, "push", "abc", "", "", false))
	assert.True(t, config.MatchesAnyTrigger(a, "push", "abbc", "", "", false))
}
