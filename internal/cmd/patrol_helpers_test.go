package cmd

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/testutil"
)

func TestBuildWitnessPatrolVars_NilContext(t *testing.T) {
	ctx := RoleContext{}
	vars := buildWitnessPatrolVars(ctx)
	if len(vars) != 0 {
		t.Errorf("expected empty vars for nil context, got %v", vars)
	}
}

func TestBuildWitnessPatrolVars_InjectsRigAndPrefix(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildWitnessPatrolVars(ctx)
	if len(vars) != 2 {
		t.Fatalf("expected 2 vars (rig, prefix), got %v", vars)
	}
	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}
	if got := varMap["rig"]; got != "testrig" {
		t.Errorf("rig = %q, want %q", got, "testrig")
	}
	if got := varMap["prefix"]; got != "gt" {
		t.Errorf("prefix = %q, want %q (default fallback)", got, "gt")
	}
}

func TestBuildRefineryPatrolVars_NilContext(t *testing.T) {
	ctx := RoleContext{}
	vars := buildRefineryPatrolVars(ctx)
	if len(vars) != 0 {
		t.Errorf("expected empty vars for nil context, got %v", vars)
	}
}

func TestBuildRefineryPatrolVars_MissingSettings(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(filepath.Join(rigDir, "settings"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildRefineryPatrolVars(ctx)
	// rig and target_branch should always be present.
	if len(vars) != 2 {
		t.Errorf("expected 2 vars (rig, target_branch) when settings file missing, got %v", vars)
	}
	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}
	if got := varMap["rig"]; got != "testrig" {
		t.Errorf("rig = %q, want %q", got, "testrig")
	}
	if got := varMap["target_branch"]; got != "main" {
		t.Errorf("target_branch = %q, want %q", got, "main")
	}
}

func TestAutoSpawnPatrol_RefinerySafetyStoppedSkipsWispCreate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mock bd script uses POSIX shell")
	}
	townRoot := setupRefinerySafetyStopTown(t)
	logPath := installRefinerySafetyStopMockBD(t)

	_, err := autoSpawnPatrol(PatrolConfig{
		RoleName:      "refinery",
		PatrolMolName: constants.MolRefineryPatrol,
		BeadsDir:      townRoot,
		Assignee:      "testrig/refinery",
	})
	if !errors.Is(err, refinery.ErrSafetyStopped) {
		t.Fatalf("autoSpawnPatrol error = %v, want ErrSafetyStopped", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	if strings.Contains(string(logData), "mol wisp create") || strings.Contains(string(logData), "update ") {
		t.Fatalf("autoSpawnPatrol mutated patrol state despite safety stop; log:\n%s", logData)
	}
}

func setupRefinerySafetyStopTown(t *testing.T) string {
	t.Helper()
	townRoot := t.TempDir()
	for _, dir := range []string{filepath.Join(townRoot, "mayor"), filepath.Join(townRoot, ".beads"), filepath.Join(townRoot, "testrig")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0o644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	return townRoot
}

func installRefinerySafetyStopMockBD(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done
case "$cmd" in
  version)
    echo "bd test"
    ;;
  show)
    printf '%s\n' '[{"id":"gt-testrig-refinery","title":"Refinery","issue_type":"task","labels":["gt:agent","safety_stop:hq-vmrwr"],"status":"open","description":"role_type: refinery\nrig: testrig\nagent_state: idle"}]'
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func TestBuildRefineryPatrolVars_NilMergeQueue(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write settings with no merge_queue
	settings := config.RigSettings{
		Type:    "rig-settings",
		Version: 1,
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildRefineryPatrolVars(ctx)
	// rig and target_branch should always be present.
	if len(vars) != 2 {
		t.Errorf("expected 2 vars (rig, target_branch) when merge_queue is nil, got %v", vars)
	}
	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}
	if got := varMap["rig"]; got != "testrig" {
		t.Errorf("rig = %q, want %q", got, "testrig")
	}
	if got := varMap["target_branch"]; got != "main" {
		t.Errorf("target_branch = %q, want %q", got, "main")
	}
}

func TestBuildRefineryPatrolVars_FullConfig(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write rig config.json with default_branch (source of truth for default branch)
	rigConfig := map[string]interface{}{"type": "rig", "version": 1, "name": "testrig"}
	rigData, _ := json.Marshal(rigConfig)
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), rigData, 0o644); err != nil {
		t.Fatal(err)
	}

	mq := config.DefaultMergeQueueConfig()
	settings := config.RigSettings{
		Type:       "rig-settings",
		Version:    1,
		MergeQueue: mq,
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildRefineryPatrolVars(ctx)

	// DefaultMergeQueueConfig: refinery_enabled=true, auto_land=false, run_tests=true,
	// test_command="" (language-agnostic), target_branch="main" (from rig config),
	// delete_merged_branches=true, judgment_enabled=false, review_depth="standard"
	// merge_strategy is omitted when not explicitly set (formula default "direct" applies)
	// New commands (setup, typecheck, lint, build) default to empty = omitted
	// judgment_enabled defaults to false, review_depth defaults to "standard"
	expected := map[string]string{
		"rig":                                 "testrig",
		"integration_branch_refinery_enabled": "true",
		"integration_branch_auto_land":        "false",
		"run_tests":                           "true",
		"target_branch":                       "main",
		"delete_merged_branches":              "true",
		"judgment_enabled":                    "false",
		"review_depth":                        "standard",
		"require_review":                      "false",
	}

	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}

	for key, want := range expected {
		got, ok := varMap[key]
		if !ok {
			t.Errorf("missing var %q", key)
			continue
		}
		if got != want {
			t.Errorf("var %q = %q, want %q", key, got, want)
		}
	}

	// Verify empty commands and unset strategy are NOT included
	for _, shouldBeAbsent := range []string{"setup_command", "typecheck_command", "lint_command", "build_command", "merge_strategy"} {
		if _, ok := varMap[shouldBeAbsent]; ok {
			t.Errorf("%q should be omitted when empty/unset", shouldBeAbsent)
		}
	}

	if len(vars) != len(expected) {
		t.Errorf("expected %d vars, got %d: %v", len(expected), len(vars), vars)
	}
}

func TestBuildRefineryPatrolVars_AllCommandsSet(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mq := config.DefaultMergeQueueConfig()
	mq.SetupCommand = "pnpm install"
	mq.TypecheckCommand = "tsc --noEmit"
	mq.LintCommand = "eslint ."
	mq.BuildCommand = "pnpm build"
	settings := config.RigSettings{
		Type:       "rig-settings",
		Version:    1,
		MergeQueue: mq,
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildRefineryPatrolVars(ctx)

	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}

	// All configured commands should be present (test_command is empty by default)
	commandExpected := map[string]string{
		"setup_command":     "pnpm install",
		"typecheck_command": "tsc --noEmit",
		"lint_command":      "eslint .",
		"build_command":     "pnpm build",
	}
	for key, want := range commandExpected {
		got, ok := varMap[key]
		if !ok {
			t.Errorf("missing var %q", key)
			continue
		}
		if got != want {
			t.Errorf("var %q = %q, want %q", key, got, want)
		}
	}
}

func TestBuildRefineryPatrolVars_EmptyTestCommand(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	falseVal := false
	trueVal2 := true
	mq := &config.MergeQueueConfig{
		Enabled:              true,
		RunTests:             &falseVal,
		TestCommand:          "", // empty - should be omitted
		DeleteMergedBranches: &trueVal2,
	}
	settings := config.RigSettings{
		Type:       "rig-settings",
		Version:    1,
		MergeQueue: mq,
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildRefineryPatrolVars(ctx)

	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}

	// test_command should not be present when empty
	if _, ok := varMap["test_command"]; ok {
		t.Error("test_command should be omitted when empty")
	}

	// All command vars should be omitted when empty
	for _, cmd := range []string{"setup_command", "typecheck_command", "lint_command", "build_command"} {
		if _, ok := varMap[cmd]; ok {
			t.Errorf("%q should be omitted when empty", cmd)
		}
	}

	// run_tests should be "false"
	if got := varMap["run_tests"]; got != "false" {
		t.Errorf("run_tests = %q, want %q", got, "false")
	}
}

func TestBuildRefineryPatrolVars_BoolFormat(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write rig config.json with default_branch = "develop"
	rigConfig := map[string]interface{}{"type": "rig", "version": 1, "name": "testrig", "default_branch": "develop"}
	rigData, _ := json.Marshal(rigConfig)
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), rigData, 0o644); err != nil {
		t.Fatal(err)
	}

	trueVal := true
	falseVal2 := false
	mq := &config.MergeQueueConfig{
		Enabled:                          true,
		IntegrationBranchAutoLand:        &trueVal,
		IntegrationBranchRefineryEnabled: &trueVal,
		RunTests:                         &trueVal,
		SetupCommand:                     "npm ci",
		TypecheckCommand:                 "tsc --noEmit",
		LintCommand:                      "eslint .",
		TestCommand:                      "make test",
		BuildCommand:                     "make build",
		DeleteMergedBranches:             &falseVal2,
	}
	settings := config.RigSettings{
		Type:       "rig-settings",
		Version:    1,
		MergeQueue: mq,
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildRefineryPatrolVars(ctx)

	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}

	// Check bool format is "true"/"false" strings
	if got := varMap["integration_branch_auto_land"]; got != "true" {
		t.Errorf("integration_branch_auto_land = %q, want %q", got, "true")
	}
	if got := varMap["delete_merged_branches"]; got != "false" {
		t.Errorf("delete_merged_branches = %q, want %q", got, "false")
	}
	if got := varMap["target_branch"]; got != "develop" {
		t.Errorf("target_branch = %q, want %q", got, "develop")
	}
	if got := varMap["test_command"]; got != "make test" {
		t.Errorf("test_command = %q, want %q", got, "make test")
	}
	if got := varMap["setup_command"]; got != "npm ci" {
		t.Errorf("setup_command = %q, want %q", got, "npm ci")
	}
	if got := varMap["typecheck_command"]; got != "tsc --noEmit" {
		t.Errorf("typecheck_command = %q, want %q", got, "tsc --noEmit")
	}
	if got := varMap["lint_command"]; got != "eslint ." {
		t.Errorf("lint_command = %q, want %q", got, "eslint .")
	}
	if got := varMap["build_command"]; got != "make build" {
		t.Errorf("build_command = %q, want %q", got, "make build")
	}
}

func TestBuildRefineryPatrolVars_DefaultBranchWithoutMQ(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write rig config with custom default_branch but NO settings/config.json
	rigConfig := map[string]interface{}{
		"type": "rig", "version": 1, "name": "testrig",
		"default_branch": "gastown",
	}
	rigData, _ := json.Marshal(rigConfig)
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), rigData, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildRefineryPatrolVars(ctx)

	// rig and target_branch must be present even without merge_queue settings.
	if len(vars) != 2 {
		t.Errorf("expected 2 vars (rig, target_branch), got %d: %v", len(vars), vars)
	}
	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}
	if got := varMap["rig"]; got != "testrig" {
		t.Errorf("rig = %q, want %q", got, "testrig")
	}
	if got := varMap["target_branch"]; got != "gastown" {
		t.Errorf("target_branch = %q, want %q (should read rig config even without MQ settings)", got, "gastown")
	}
}

func TestBuildRefineryPatrolVars_MergeStrategy(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mq := config.DefaultMergeQueueConfig()
	mq.MergeStrategy = "pr"
	settings := config.RigSettings{
		Type:       "rig-settings",
		Version:    1,
		MergeQueue: mq,
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildRefineryPatrolVars(ctx)

	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}

	if got := varMap["merge_strategy"]; got != "pr" {
		t.Errorf("merge_strategy = %q, want %q (rig-level config must override formula default)", got, "pr")
	}
}

func TestBuildRefineryPatrolVars_MergeStrategyDefaultOmitted(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// MergeStrategy not set — should not be injected (formula default "direct" applies)
	mq := config.DefaultMergeQueueConfig()
	settings := config.RigSettings{
		Type:       "rig-settings",
		Version:    1,
		MergeQueue: mq,
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildRefineryPatrolVars(ctx)

	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}

	// merge_strategy should be absent when not explicitly configured
	if _, ok := varMap["merge_strategy"]; ok {
		t.Error("merge_strategy should be omitted when not configured (let formula default apply)")
	}
}

func TestBuildRefineryPatrolVars_RequireReview(t *testing.T) {
	tmpDir := t.TempDir()
	rigDir := filepath.Join(tmpDir, "testrig")
	settingsDir := filepath.Join(rigDir, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mq := config.DefaultMergeQueueConfig()
	mq.MergeStrategy = "pr"
	requireReview := true
	mq.RequireReview = &requireReview
	settings := config.RigSettings{
		Type:       "rig-settings",
		Version:    1,
		MergeQueue: mq,
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := RoleContext{
		TownRoot: tmpDir,
		Rig:      "testrig",
	}
	vars := buildRefineryPatrolVars(ctx)

	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitFirstEquals(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}

	if got := varMap["require_review"]; got != "true" {
		t.Errorf("require_review = %q, want %q", got, "true")
	}
	if got := varMap["merge_strategy"]; got != "pr" {
		t.Errorf("merge_strategy = %q, want %q", got, "pr")
	}
}

// splitFirstEquals splits a string on the first '=' only.
func splitFirstEquals(s string) []string {
	idx := -1
	for i, c := range s {
		if c == '=' {
			idx = i
			break
		}
	}
	if idx < 0 {
		return []string{s}
	}
	return []string{s[:idx], s[idx+1:]}
}

func TestPatrolRigName(t *testing.T) {
	if got := patrolRigName(PatrolConfig{Assignee: "gastown/refinery"}); got != "gastown" {
		t.Fatalf("patrolRigName = %q, want gastown", got)
	}
	if got := patrolRigName(PatrolConfig{Assignee: "deacon"}); got != "" {
		t.Fatalf("patrolRigName without rig = %q, want empty", got)
	}
}

func TestRenderPatrolWispDescription_DeaconInlinesStepsAndVars(t *testing.T) {
	desc, err := renderPatrolWispDescription(PatrolConfig{
		PatrolMolName: "mol-deacon-patrol",
		BeadsDir:      t.TempDir(),
		Assignee:      "deacon",
		ExtraVars:     []string{"idle_effort_threshold=7"},
	})
	if err != nil {
		t.Fatalf("renderPatrolWispDescription: %v", err)
	}
	for _, want := range []string{
		"Mayor's daemon patrol loop.",
		"**Formula Checklist**",
		"gt deacon heartbeat \"starting patrol cycle\"",
		"idle_cycles >= 7",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q:\n%s", want, desc)
		}
	}
}

func TestRenderPatrolWispDescription_AppliesOverlay(t *testing.T) {
	townRoot := t.TempDir()
	overlayDir := filepath.Join(townRoot, "formula-overlays")
	if err := os.MkdirAll(overlayDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "mol-deacon-patrol.toml"), []byte(`[[step-overrides]]
step_id = "heartbeat"
mode = "append"
description = "overlay heartbeat note"
`), 0644); err != nil {
		t.Fatal(err)
	}

	desc, err := renderPatrolWispDescription(PatrolConfig{
		PatrolMolName: "mol-deacon-patrol",
		BeadsDir:      townRoot,
		Assignee:      "deacon",
	})
	if err != nil {
		t.Fatalf("renderPatrolWispDescription: %v", err)
	}
	if !strings.Contains(desc, "overlay heartbeat note") {
		t.Fatalf("description did not include overlay text:\n%s", desc)
	}
}

func TestRenderPatrolWispDescription_RefinerySubstitutesRigAndEmptyDefaults(t *testing.T) {
	desc, err := renderPatrolWispDescription(PatrolConfig{
		PatrolMolName: "mol-refinery-patrol",
		BeadsDir:      t.TempDir(),
		Assignee:      "gastown/refinery",
	})
	if err != nil {
		t.Fatalf("renderPatrolWispDescription: %v", err)
	}
	if strings.Contains(desc, "{{") {
		t.Fatalf("description contains unresolved placeholder:\n%s", desc)
	}
	if !strings.Contains(desc, "gt agents resolve --role refinery --rig gastown") {
		t.Fatalf("description did not substitute refinery rig:\n%s", desc)
	}
}

func TestRenderPatrolWispDescription_ExtraVarsOverrideRoleVars(t *testing.T) {
	desc, err := renderPatrolWispDescription(PatrolConfig{
		PatrolMolName: constants.MolRefineryPatrol,
		BeadsDir:      t.TempDir(),
		Assignee:      "gastown/refinery",
		ExtraVars:     []string{"rig=override"},
	})
	if err != nil {
		t.Fatalf("renderPatrolWispDescription: %v", err)
	}
	if !strings.Contains(desc, "gt agents resolve --role refinery --rig override") {
		t.Fatalf("description did not use ExtraVars override:\n%s", desc)
	}
	if strings.Contains(desc, "gt agents resolve --role refinery --rig gastown") {
		t.Fatalf("description used role var instead of ExtraVars override:\n%s", desc)
	}
}

func TestPatrolSpawnArgsMaterializesFormulaSteps(t *testing.T) {
	args := patrolSpawnArgs("mol-witness-patrol", "witness", []string{"rig=gastown", "prefix=gt"})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--root-only") {
		t.Fatalf("patrol creation must materialize steps, got args: %s", joined)
	}
	for _, want := range []string{"mol wisp create mol-witness-patrol", "--actor witness", "--var rig=gastown", "--var prefix=gt"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("spawn args missing %q: %s", want, joined)
		}
	}
}

func TestAcquirePatrolCycleLockSerializesSuccessorHandoff(t *testing.T) {
	cfg := PatrolConfig{BeadsDir: t.TempDir(), Assignee: "testrig/witness"}
	unlockFirst, err := acquirePatrolCycleLock(cfg)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan func(), 1)
	errs := make(chan error, 1)
	go func() {
		unlock, lockErr := acquirePatrolCycleLock(cfg)
		if lockErr != nil {
			errs <- lockErr
			return
		}
		acquired <- unlock
	}()

	select {
	case unlock := <-acquired:
		unlock()
		unlockFirst()
		t.Fatal("second patrol reporter acquired the role lock before the first released it")
	case err := <-errs:
		unlockFirst()
		t.Fatal(err)
	case <-time.After(100 * time.Millisecond):
	}

	unlockFirst()
	select {
	case unlock := <-acquired:
		unlock()
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("second patrol reporter did not acquire the role lock after release")
	}
}

func TestBuildPatrolWispDescriptionCarriesExecutableAttachment(t *testing.T) {
	at := time.Date(2026, time.August, 25, 14, 4, 0, 123, time.UTC)
	desc, err := buildPatrolWispDescription(PatrolConfig{
		PatrolMolName: constants.MolDeaconPatrol,
		BeadsDir:      t.TempDir(),
		Assignee:      "deacon/",
	}, at)
	if err != nil {
		t.Fatalf("buildPatrolWispDescription: %v", err)
	}
	fields := beads.ParseAttachmentFields(&beads.Issue{Description: desc})
	if fields == nil {
		t.Fatalf("patrol description has no attachment metadata:\n%s", desc)
	}
	if fields.AttachedFormula != constants.MolDeaconPatrol {
		t.Fatalf("attached_formula = %q, want %q", fields.AttachedFormula, constants.MolDeaconPatrol)
	}
	if fields.AttachedMolecule != "" {
		t.Fatalf("standalone patrol must not self-reference attached_molecule: %q", fields.AttachedMolecule)
	}
	if fields.AttachedAt != at.Format(time.RFC3339Nano) {
		t.Fatalf("attached_at = %q, want %q", fields.AttachedAt, at.Format(time.RFC3339Nano))
	}
	for _, want := range []string{"wisp: true", "instantiated_from: " + constants.MolDeaconPatrol, "**Formula Checklist**"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("patrol description missing %q:\n%s", want, desc)
		}
	}
}

func TestUpdatePatrolWispDescriptionUsesBodyFileStdin(t *testing.T) {
	binDir := t.TempDir()
	logFile := filepath.Join(t.TempDir(), "bd.log")
	writeBDStub(t, binDir, `#!/usr/bin/env sh
{
  printf 'args:%s\n' "$*"
  printf 'stdin:'
  cat
  printf '\n'
} >> "$BD_STUB_LOG"
`, `@echo off
echo args:%* >> %BD_STUB_LOG%
set /p stdin=
echo stdin:%stdin% >> %BD_STUB_LOG%
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_STUB_LOG", logFile)

	err := updatePatrolWispDescription(PatrolConfig{BeadsDir: t.TempDir()}, filepath.Join(t.TempDir(), ".beads"), "gt-wisp-test", "line one\nline two")
	if err != nil {
		t.Fatalf("updatePatrolWispDescription: %v", err)
	}
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if !strings.Contains(log, "args:update gt-wisp-test --body-file=-") {
		t.Fatalf("expected body-file update args, got:\n%s", log)
	}
	if strings.Contains(log, "--description=") {
		t.Fatalf("description must not be passed through argv:\n%s", log)
	}
	if !strings.Contains(log, "stdin:line one\nline two") {
		t.Fatalf("expected multiline description on stdin, got:\n%s", log)
	}
}

// --- Patrol discovery tests (findActivePatrol) ---

func requireBd(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd CLI not installed, skipping patrol test")
	}
}

func setupPatrolTestDB(t *testing.T) (string, *beads.Beads) {
	t.Helper()
	testutil.RequireDoltContainer(t)
	port, _ := strconv.Atoi(testutil.DoltContainerPort())
	tmpDir := t.TempDir()
	b := beads.NewIsolatedWithPort(tmpDir, port)
	// Use a unique prefix per test run to avoid cross-run contamination
	// in the shared Dolt database.
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	prefix := "pt" + hex.EncodeToString(buf[:])
	if err := b.Init(prefix); err != nil {
		t.Fatalf("bd init: %v", err)
	}

	// Clean up the test database after the test to avoid leaking
	// beads_pt* databases on the shared Dolt server.
	dbName := "beads_" + prefix
	t.Cleanup(func() {
		dsn := fmt.Sprintf("root:@tcp(127.0.0.1:%s)/", testutil.DoltContainerPort())
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			t.Logf("cleanup: failed to connect to dolt server to drop %s: %v", dbName, err)
			return
		}
		defer db.Close()
		if _, err := db.Exec("DROP DATABASE IF EXISTS `" + dbName + "`"); err != nil {
			t.Logf("cleanup: failed to drop %s: %v", dbName, err)
		}
		// Purge dropped databases to prevent accumulation on disk
		db.Exec("CALL dolt_purge_dropped_databases()") //nolint:errcheck
	})

	return tmpDir, b
}

// createHookedPatrol creates a bead with a patrol title and hooks it.
// If withOpenChild is true, creates an open child bead to simulate an active patrol.
func createHookedPatrol(t *testing.T, b *beads.Beads, molName, assignee string, withOpenChild bool) string {
	t.Helper()
	root, err := b.Create(beads.CreateOptions{
		Title:    molName + " (wisp)",
		Priority: -1,
	})
	if err != nil {
		t.Fatalf("create patrol root: %v", err)
	}

	hooked := beads.StatusHooked
	if err := b.Update(root.ID, beads.UpdateOptions{
		Status:   &hooked,
		Assignee: &assignee,
	}); err != nil {
		t.Fatalf("hook patrol: %v", err)
	}

	if withOpenChild {
		_, err := b.Create(beads.CreateOptions{
			Title:    "inbox-check",
			Parent:   root.ID,
			Priority: -1,
		})
		if err != nil {
			t.Fatalf("create child: %v", err)
		}
	}
	return root.ID
}

func TestValidatePatrolMaterializationRequiresEveryFormulaStep(t *testing.T) {
	requireBd(t)
	tmpDir, b := setupPatrolTestDB(t)
	cfg := PatrolConfig{
		RoleName:      "deacon",
		PatrolMolName: constants.MolDeaconPatrol,
		BeadsDir:      tmpDir,
		Assignee:      "deacon/",
		Beads:         b,
	}
	f, _, err := resolveFormulaForRendering(cfg.PatrolMolName, cfg.BeadsDir, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	stepIDs := f.GetAllIDs()
	if len(stepIDs) < 2 {
		t.Fatalf("test requires multiple patrol steps, got %v", stepIDs)
	}
	root, err := b.Create(beads.CreateOptions{Title: cfg.PatrolMolName + " (wisp)", Priority: -1})
	if err != nil {
		t.Fatal(err)
	}
	createStep := func(stepID string) {
		t.Helper()
		if _, createErr := b.Create(beads.CreateOptions{
			Title:       stepID,
			Description: "instantiated_from: " + cfg.PatrolMolName + "\ntemplate_step: " + stepID,
			Parent:      root.ID,
			Priority:    -1,
		}); createErr != nil {
			t.Fatal(createErr)
		}
	}
	createStep(stepIDs[0])
	if err := validatePatrolMaterialization(cfg, root.ID); err == nil || !strings.Contains(err.Error(), "1/") {
		t.Fatalf("partial materialization error = %v, want step-count failure", err)
	}
	for _, stepID := range stepIDs[1:] {
		createStep(stepID)
	}
	if err := validatePatrolMaterialization(cfg, root.ID); err != nil {
		t.Fatalf("complete materialization rejected: %v", err)
	}
}

func TestFailClosedPatrolClosesNewRootAndSteps(t *testing.T) {
	requireBd(t)
	tmpDir, b := setupPatrolTestDB(t)
	rootID := createHookedPatrol(t, b, "mol-test-patrol", "testrig/witness", true)
	cfg := PatrolConfig{BeadsDir: tmpDir, Beads: b}
	if err := failClosedPatrol(cfg, rootID, "test partial failure"); err != nil {
		t.Fatalf("failClosedPatrol: %v", err)
	}
	root, err := b.Show(rootID)
	if err != nil {
		t.Fatal(err)
	}
	if root.Status != "closed" {
		t.Fatalf("partial root status = %q, want closed", root.Status)
	}
	children, err := b.List(beads.ListOptions{Parent: rootID, Status: "all", Priority: -1})
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range children {
		if child.Status != "closed" {
			t.Fatalf("partial child %s status = %q, want closed", child.ID, child.Status)
		}
	}
}

func TestFindActivePatrolHooked(t *testing.T) {
	requireBd(t)
	tmpDir, b := setupPatrolTestDB(t)

	molName := "mol-test-patrol"
	assignee := "testrig/witness"

	rootID := createHookedPatrol(t, b, molName, assignee, true /* withOpenChild */)

	cfg := PatrolConfig{
		PatrolMolName: molName,
		BeadsDir:      tmpDir,
		Assignee:      assignee,
		Beads:         b,
	}

	patrolID, _, found, findErr := findActivePatrol(cfg)
	if findErr != nil {
		t.Fatalf("findActivePatrol error: %v", findErr)
	}
	if !found {
		t.Fatal("expected to find active patrol, got not found")
	}
	if patrolID != rootID {
		t.Errorf("patrolID = %q, want %q", patrolID, rootID)
	}

	// Verify the patrol is still hooked (not closed)
	issue, err := b.Show(rootID)
	if err != nil {
		t.Fatalf("show patrol: %v", err)
	}
	if issue.Status != beads.StatusHooked {
		t.Errorf("patrol status = %q, want %q", issue.Status, beads.StatusHooked)
	}
}

func TestFindActivePatrolStale(t *testing.T) {
	requireBd(t)
	tmpDir, b := setupPatrolTestDB(t)

	molName := "mol-test-patrol"
	assignee := "testrig/witness"

	// Create a patrol with a closed child (simulates post-squash state)
	rootID := createHookedPatrol(t, b, molName, assignee, true /* with child */)

	// Close the child to make the patrol stale
	children, err := b.List(beads.ListOptions{Parent: rootID, Status: "all", Priority: -1})
	if err != nil {
		t.Fatalf("list children: %v", err)
	}
	for _, child := range children {
		if closeErr := b.ForceCloseWithReason("test cleanup", child.ID); closeErr != nil {
			t.Fatalf("close child: %v", closeErr)
		}
	}

	cfg := PatrolConfig{
		PatrolMolName: molName,
		BeadsDir:      tmpDir,
		Assignee:      assignee,
		Beads:         b,
	}

	_, _, found, findErr := findActivePatrol(cfg)
	if findErr != nil {
		t.Fatalf("findActivePatrol error: %v", findErr)
	}
	if found {
		t.Fatal("expected stale patrol (all children closed) to NOT be found as active")
	}

	// Discovery must preserve historical patrol evidence.
	issue, err := b.Show(rootID)
	if err != nil {
		t.Fatalf("show patrol: %v", err)
	}
	if issue.Status != beads.StatusHooked {
		t.Errorf("stale patrol status = %q, want %q (discovery is read-only)", issue.Status, beads.StatusHooked)
	}
}

func TestFindActivePatrolZeroChildren(t *testing.T) {
	requireBd(t)
	tmpDir, b := setupPatrolTestDB(t)

	molName := "mol-test-patrol"
	assignee := "testrig/witness"

	// A hooked root with no children reproduces the historical naked-hook bug.
	rootID := createHookedPatrol(t, b, molName, assignee, false /* no children */)

	cfg := PatrolConfig{
		PatrolMolName: molName,
		BeadsDir:      tmpDir,
		Assignee:      assignee,
		Beads:         b,
	}

	_, _, found, findErr := findActivePatrol(cfg)
	if findErr != nil {
		t.Fatalf("findActivePatrol error: %v", findErr)
	}
	if found {
		t.Fatal("zero-children patrol must not become authoritative hooked work")
	}

	// Verify it was NOT closed
	issue, err := b.Show(rootID)
	if err != nil {
		t.Fatalf("show patrol: %v", err)
	}
	if issue.Status != beads.StatusHooked {
		t.Errorf("zero-children patrol status = %q, want %q (historical evidence must remain untouched)", issue.Status, beads.StatusHooked)
	}
}

func TestFindActivePatrolMultiple(t *testing.T) {
	requireBd(t)
	tmpDir, b := setupPatrolTestDB(t)

	molName := "mol-test-patrol"
	assignee := "testrig/witness"

	// Create 2 stale patrols (with closed children) and 1 active patrol (with open child)
	stale1 := createHookedPatrol(t, b, molName, assignee, true)
	stale2 := createHookedPatrol(t, b, molName, assignee, true)
	activeID := createHookedPatrol(t, b, molName, assignee, true)

	// Close children of stale patrols to make them stale
	for _, staleID := range []string{stale1, stale2} {
		children, err := b.List(beads.ListOptions{Parent: staleID, Status: "all", Priority: -1})
		if err != nil {
			t.Fatalf("list children of %s: %v", staleID, err)
		}
		for _, child := range children {
			if closeErr := b.ForceCloseWithReason("test cleanup", child.ID); closeErr != nil {
				t.Fatalf("close child: %v", closeErr)
			}
		}
	}

	cfg := PatrolConfig{
		PatrolMolName: molName,
		BeadsDir:      tmpDir,
		Assignee:      assignee,
		Beads:         b,
	}

	patrolID, _, found, findErr := findActivePatrol(cfg)
	if findErr != nil {
		t.Fatalf("findActivePatrol error: %v", findErr)
	}
	if !found {
		t.Fatal("expected to find active patrol")
	}
	if patrolID != activeID {
		t.Errorf("patrolID = %q, want %q (should return the active one)", patrolID, activeID)
	}

	// Verify active patrol is still hooked
	issue, err := b.Show(activeID)
	if err != nil {
		t.Fatalf("show active: %v", err)
	}
	if issue.Status != beads.StatusHooked {
		t.Errorf("active patrol status = %q, want %q", issue.Status, beads.StatusHooked)
	}

	// Selection is read-only: historical roots remain untouched.
	for _, id := range []string{stale1, stale2} {
		staleIssue, showErr := b.Show(id)
		if showErr != nil {
			t.Fatalf("show stale %s: %v", id, showErr)
		}
		if staleIssue.Status != beads.StatusHooked {
			t.Errorf("stale patrol %s status = %q, want hooked", id, staleIssue.Status)
		}
	}
}

func TestFindActivePatrol_PreservesHistoricalWisps(t *testing.T) {
	requireBd(t)
	tmpDir, b := setupPatrolTestDB(t)

	molName := "mol-test-patrol"
	assignee := "testrig/witness"

	const numStale = 8
	staleIDs := make([]string, numStale)
	for i := 0; i < numStale; i++ {
		id := createHookedPatrol(t, b, molName, assignee, true /* with child */)
		staleIDs[i] = id

		// Close the child to make the patrol stale
		children, err := b.List(beads.ListOptions{Parent: id, Status: "all", Priority: -1})
		if err != nil {
			t.Fatalf("list children of %s: %v", id, err)
		}
		for _, child := range children {
			if closeErr := b.ForceCloseWithReason("test cleanup", child.ID); closeErr != nil {
				t.Fatalf("close child of %s: %v", id, closeErr)
			}
		}
	}

	cfg := PatrolConfig{
		PatrolMolName: molName,
		BeadsDir:      tmpDir,
		Assignee:      assignee,
		Beads:         b,
	}

	_, _, found, findErr := findActivePatrol(cfg)
	if findErr != nil {
		t.Fatalf("findActivePatrol error: %v", findErr)
	}
	if found {
		t.Fatal("expected no active patrol (all stale)")
	}

	// Discovery must not mutate or burn historical roots.
	for _, id := range staleIDs {
		issue, err := b.Show(id)
		if err != nil {
			t.Fatalf("show stale %s: %v", id, err)
		}
		if issue.Status != beads.StatusHooked {
			t.Errorf("historical patrol %s status = %q, want hooked", id, issue.Status)
		}
	}
}
