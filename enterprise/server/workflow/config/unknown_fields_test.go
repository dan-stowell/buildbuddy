package config_test

import (
	"bytes"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/workflow/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v2"
)

// Misspelled keys in buildbuddy.yaml should be reported as errors rather than
// silently producing a config that runs nothing.
func TestNewConfig_UnknownFieldsAreRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{"top_level_actons", `
actons:
  - name: "Test"
    triggers: { push: { branches: [ "main" ] } }
    steps: [ { run: "bazel test //..." } ]
`},
		{"trigger_pussh", `
actions:
  - name: "Test"
    triggers: { pussh: { branches: [ "main" ] } }
    steps: [ { run: "bazel test //..." } ]
`},
		{"pull_request_branchs", `
actions:
  - name: "Test"
    triggers: { pull_request: { branchs: [ "*" ] } }
    steps: [ { run: "bazel test //..." } ]
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.NewConfig(bytes.NewReader([]byte(tc.yaml)))
			if err == nil {
				n := len(cfg.Actions)
				var trig any
				if n > 0 {
					trig = *cfg.Actions[0].GetTriggers()
				}
				t.Logf("parsed without error: %d action(s), first action triggers=%+v", n, trig)
			}
			assert.Error(t, err, "unknown field in buildbuddy.yaml was silently ignored")
		})
	}
}

// Compatibility check (expected to pass): strict decoding accepts every
// buildbuddy.yaml example from docs/*.md, and rejects a top-level YAML anchor
// holder key (a pattern some users may rely on).
func TestStrictDecode_DocsExamples(t *testing.T) {
	examples := map[string]string{
		"workflows-config.md:41": `
actions:
  - name: "Test all targets"
    triggers:
      push:
        branches:
          - "main"
      pull_request:
        branches:
          - "*"
    steps:
      - run: "bazel test //..."
`,
		"workflows-config.md:104": `
actions:
  - name: "Test all targets"
    allow_concurrent_runs: true
`,
		"workflows-config.md:120": `
actions:
  - name: "Test all targets"
    allow_concurrent_runs: false
    allow_concurrent_runs_on_branches:
      - "main"
      - "release-*"
      - "staging"
`,
		"workflows-config.md:133": `
actions:
  - name: "Test all targets"
    allow_concurrent_runs: false
    allow_concurrent_runs_on_branches: []
`,
		"workflows-config.md:224": `
actions:
  - name: "Nightly tests"
    triggers:
      schedule:
        crons:
          - "0 2 * * *"
    steps:
      - run: "bazel test //..."
`,
		"workflows-config.md:295": `
actions:
  - name: Test
    triggers:
      push:
        branches: ["main", "gh-readonly-queue/*"]
`,
		"workflows-config.md:316": `
actions:
  - name: "Test"
    triggers:
      pull_request:
        branches: ["main"]
        merge_with_base: true
    steps:
      - run: "bazel test //..."
`,
		"workflows-config.md:417": `
actions:
  - name: "Test all targets"
    container_image: "ubuntu-24.04"
    steps:
      - run: "bazel test //..."
`,
		"workflows-config.md:445": `
actions:
  - name: Test
    steps:
      - run: |
          echo a
      - run: |
          bazel test //some/target/that/needs:libexample
`,
		"workflows-config.md:514": `
actions:
  - name: "Test all targets (Mac)"
    os: "darwin"
    arch: "arm64"
    triggers:
      push:
        branches:
          - "main"
      pull_request:
        branches:
          - "*"
    steps:
      - run: "bazel test //..."
`,
		"remote-runner-introduction.md:213": `
actions:
  - name: "Test all targets"
    platform_properties:
      remote-snapshot-save-policy: none-available
    env:
      GIT_REPO_DEFAULT_BRANCH: main
`,
		"cli-commands.md:171": `
actions:
  - name: Nondeterminism check
    triggers:
      schedule:
        crons:
          - "0 8 * * *"
    steps:
      - run: bb detect nondeterminism --notify_email
    platform_properties:
      recycle-runner: false
`,
		"all_reference_fields": `
actions:
  - name: x
    triggers:
      push: { branches: [a], tags: [b] }
      pull_request: { branches: [a], types: [opened], skip_drafts: true, merge_with_base: true, merge_with_base_interval: 3h, force_manual_merge_with_base: false }
      schedule: { crons: ["0 2 * * *"] }
    os: linux
    arch: amd64
    pool: p
    self_hosted: false
    container_image: ubuntu-22.04
    resource_requests: { memory: 1GB, cpu: 2, disk: 10GB }
    priority: 1
    user: buildbuddy
    git_clean_exclude: [a]
    git_fetch_filters: [blob:none]
    git_fetch_depth: 1
    bazel_workspace_dir: sub
    env: { A: b }
    visibility: PRIVATE
    platform_properties: { a: b }
    steps: [ { run: x } ]
    timeout: 1h
    bazel_use_cli: true
    allow_concurrent_runs: false
    allow_concurrent_runs_on_branches: [main]
    bazel_commands: [ "test //..." ]
`,
		"merge_key_anchor_in_action": `
actions:
  - &base
    name: a
    os: linux
  - <<: *base
    name: b
`,
	}
	for name, y := range examples {
		cfg := &config.BuildBuddyConfig{}
		err := yaml.UnmarshalStrict([]byte(y), cfg)
		require.NoError(t, err, "strict decode of %s", name)
		require.NotEmpty(t, cfg.Actions, name)
	}

	// A top-level anchor holder key is accepted today but rejected by strict decoding.
	anchorHolder := `
x-common: &common
  os: linux
actions:
  - <<: *common
    name: a
`
	_, err := config.NewConfig(bytes.NewReader([]byte(anchorHolder)))
	require.NoError(t, err, "lenient decode")
	err = yaml.UnmarshalStrict([]byte(anchorHolder), &config.BuildBuddyConfig{})
	t.Logf("strict decode of top-level anchor holder: %v", err)
	require.Error(t, err)
}
