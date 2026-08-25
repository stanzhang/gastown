package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
)

func TestFindAgentBeadCandidatesResolvesSameTownIdentityFromTownAndRigCwd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fake bd")
	}

	townRoot, _ := filepath.EvalSymlinks(t.TempDir())
	townBeadsDir := filepath.Join(townRoot, ".beads")
	rigDir := filepath.Join(townRoot, "gastown", "refinery", "rig")
	for _, dir := range []string{filepath.Join(townRoot, "mayor"), townBeadsDir, rigDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0o644); err != nil {
		t.Fatalf("write town marker: %v", err)
	}

	binDir := t.TempDir()
	script := `#!/bin/sh
cmd=""
for arg in "$@"; do case "$arg" in --*) ;; *) cmd="$arg"; break ;; esac; done
case "$cmd" in
  version) printf 'bd version 1.2.2\n' ;;
  list) printf '%s\n' '[{"id":"gt-gastown-refinery","status":"open","labels":["gt:agent"],"description":"role_type: refinery\nrig: gastown"}]' ;;
  query) printf '%s\n' '[]' ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	for _, cwd := range []string{townRoot, rigDir} {
		candidates, err := findAgentBeadCandidates(cwd, townBeadsDir)
		if err != nil {
			t.Fatalf("find candidates from %s: %v", cwd, err)
		}
		var matches []agentBeadCandidate
		for _, candidate := range candidates {
			if agentBeadMatches(candidate.Issue, "refinery", "gastown") {
				matches = append(matches, candidate)
			}
		}
		got, err := pickBestAgentBead(matches)
		if err != nil {
			t.Fatalf("pick candidate from %s: %v", cwd, err)
		}
		if got == nil || got.ID != "gt-gastown-refinery" || got.Source != agentSourceTownIssues {
			t.Fatalf("candidate from %s = %#v, want canonical town identity", cwd, got)
		}
	}
}

func TestAgentBeadMatchesDescriptionAndIDFallback(t *testing.T) {
	tests := []struct {
		name  string
		issue *beads.Issue
		role  string
		rig   string
		want  bool
	}{
		{
			name: "description matches legacy random wisp ID",
			issue: &beads.Issue{
				ID:          "au-wisp-0ti",
				Description: "Agent\n\nrole_type: refinery\nrig: alleago_ui",
			},
			role: "refinery",
			rig:  "alleago_ui",
			want: true,
		},
		{
			name: "canonical ID fallback matches sparse wisp metadata",
			issue: &beads.Issue{
				ID: "gt-gastown-witness",
			},
			role: "witness",
			rig:  "gastown",
			want: true,
		},
		{
			name: "collapsed prefix-rig ID fallback matches sparse metadata",
			issue: &beads.Issue{
				ID: "cp-refinery",
			},
			role: "refinery",
			rig:  "cp",
			want: true,
		},
		{
			name: "role mismatch",
			issue: &beads.Issue{
				ID:          "gt-gastown-witness",
				Description: "Agent\n\nrole_type: witness\nrig: gastown",
			},
			role: "refinery",
			rig:  "gastown",
			want: false,
		},
		{
			name: "rig mismatch",
			issue: &beads.Issue{
				ID:          "gt-gastown-refinery",
				Description: "Agent\n\nrole_type: refinery\nrig: gastown",
			},
			role: "refinery",
			rig:  "other",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := agentBeadMatches(tt.issue, tt.role, tt.rig)
			if got != tt.want {
				t.Fatalf("agentBeadMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAgentsResolveUsesTownRegistryAcrossPatrolContexts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fake bd")
	}

	townRoot := filepath.Join(t.TempDir(), "gt")
	townBeads := filepath.Join(townRoot, ".beads")
	contexts := map[string]string{
		"town":     townRoot,
		"rig root": filepath.Join(townRoot, "queen_annes_revenge"),
		"witness":  filepath.Join(townRoot, "queen_annes_revenge", "witness"),
		"refinery": filepath.Join(townRoot, "queen_annes_revenge", "refinery", "rig"),
	}
	blackpearlContexts := map[string]string{
		"town":     townRoot,
		"rig root": filepath.Join(townRoot, "blackpearl"),
		"witness":  filepath.Join(townRoot, "blackpearl", "witness"),
		"refinery": filepath.Join(townRoot, "blackpearl", "refinery", "rig"),
	}

	for _, dir := range append(mapValues(contexts), mapValues(blackpearlContexts)...) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(townBeads, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(townRoot, "bd.log")
	script := `#!/bin/sh
printf 'args=%s BEADS_DIR=%s\n' "$*" "${BEADS_DIR-}" >> "$BD_LOG"
case " $* " in
*" list "*)
  printf '%s\n' '[{"id":"qar-queen_annes_revenge-refinery","status":"open","description":"role_type: refinery\nrig: queen_annes_revenge","labels":["gt:agent"]}]'
  ;;
*" query "*)
  printf '%s\n' '[]'
  ;;
*" version "*)
  printf '%s\n' 'bd version 0.0.0-test'
  ;;
*)
  printf 'unexpected mutation: %s\n' "$1" >&2
  exit 1
  ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_LOG", logPath)

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	oldRole, oldRig := agentsResolveRole, agentsResolveRig
	oldJSON, oldQuiet := agentsResolveJSON, agentsResolveQuiet
	t.Cleanup(func() {
		agentsResolveRole, agentsResolveRig = oldRole, oldRig
		agentsResolveJSON, agentsResolveQuiet = oldJSON, oldQuiet
	})
	agentsResolveJSON = false
	agentsResolveQuiet = false

	for name, cwd := range contexts {
		t.Run("QAR refinery from "+name, func(t *testing.T) {
			if err := os.Chdir(cwd); err != nil {
				t.Fatal(err)
			}
			agentsResolveRole = "refinery"
			agentsResolveRig = "queen_annes_revenge"
			var out bytes.Buffer
			command := &cobra.Command{}
			command.SetOut(&out)
			if err := runAgentsResolve(command, nil); err != nil {
				t.Fatalf("runAgentsResolve() error = %v", err)
			}
			if got := strings.TrimSpace(out.String()); got != "qar-queen_annes_revenge-refinery" {
				t.Fatalf("resolved %q, want canonical QAR refinery bead", got)
			}
		})
	}

	for name, cwd := range blackpearlContexts {
		t.Run("blackpearl witness from "+name, func(t *testing.T) {
			if err := os.Chdir(cwd); err != nil {
				t.Fatal(err)
			}
			agentsResolveRole = "witness"
			agentsResolveRig = "blackpearl"
			err := runAgentsResolve(&cobra.Command{}, nil)
			var resolveErr *agentBeadResolveError
			if !errors.As(err, &resolveErr) || resolveErr.Kind != agentBeadNotFound {
				t.Fatalf("runAgentsResolve() error = %T %v, want typed not-found", err, err)
			}
		})
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	townBeadsCanonical, err := filepath.EvalSymlinks(townBeads)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(logData)), "\n") {
		if !strings.Contains(line, "BEADS_DIR="+townBeadsCanonical) {
			t.Fatalf("lookup escaped town registry: %s", line)
		}
		if !strings.Contains(line, " list ") && !strings.Contains(line, " query ") && !strings.Contains(line, " version ") {
			t.Fatalf("resolver performed a write: %s", line)
		}
	}
}

func mapValues(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func TestPickBestAgentBead(t *testing.T) {
	candidates := []agentBeadCandidate{
		candidate("town-issue", agentSourceTownIssues, "open"),
		candidate("rig-issue", agentSourceRigIssues, "open"),
		candidate("town-wisp", agentSourceTownWisps, "open"),
		candidate("rig-wisp", agentSourceRigWisps, "open"),
	}

	got, err := pickBestAgentBead(candidates)
	if err != nil {
		t.Fatalf("pickBestAgentBead returned error: %v", err)
	}
	if got == nil || got.ID != "town-wisp" {
		t.Fatalf("pickBestAgentBead picked %v, want town-wisp", got)
	}
}

func TestPickBestAgentBeadSkipsClosed(t *testing.T) {
	candidates := []agentBeadCandidate{
		candidate("closed-rig-wisp", agentSourceRigWisps, "closed"),
		candidate("open-rig-issue", agentSourceRigIssues, "open"),
	}

	got, err := pickBestAgentBead(candidates)
	if err != nil {
		t.Fatalf("pickBestAgentBead returned error: %v", err)
	}
	if got == nil || got.ID != "open-rig-issue" {
		t.Fatalf("pickBestAgentBead picked %v, want open-rig-issue", got)
	}
}

func TestPickBestAgentBeadRejectsSameRankDuplicates(t *testing.T) {
	candidates := []agentBeadCandidate{
		candidate("rig-wisp-a", agentSourceRigWisps, "open"),
		candidate("rig-wisp-b", agentSourceRigWisps, "open"),
		candidate("rig-issue", agentSourceRigIssues, "open"),
	}

	got, err := pickBestAgentBead(candidates)
	if err == nil {
		t.Fatalf("pickBestAgentBead picked %v, want duplicate error", got)
	}
	if !strings.Contains(err.Error(), "multiple matching agent beads") {
		t.Fatalf("error = %q, want duplicate diagnostic", err)
	}
}

func candidate(id string, source agentBeadSource, status string) agentBeadCandidate {
	return agentBeadCandidate{
		ID:     id,
		Source: source,
		Status: status,
		Issue:  &beads.Issue{ID: id, Status: status},
	}
}
