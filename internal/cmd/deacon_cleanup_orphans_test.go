package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/util"
)

func TestRunDeaconCleanupOrphansDryRunSendsZeroSignals(t *testing.T) {
	assessment := util.OrphanProcessAssessment{
		Process: util.OrphanedProcess{
			PID:      4242,
			Cmd:      "claude",
			Age:      120,
			TownRoot: "/tmp/test-town",
		},
		ParentPID: 101,
		User:      "test-user",
		TTY:       "??",
		Decision:  "eligible: orphan cleanup candidate",
		Eligible:  true,
	}

	cleanupCalled := false
	var runErr error
	output := captureStdout(t, func() {
		runErr = runDeaconCleanupOrphansWith(
			true,
			func() ([]util.OrphanProcessAssessment, error) {
				return []util.OrphanProcessAssessment{assessment}, nil
			},
			func([]util.OrphanProcessAssessment) ([]util.CleanupResult, error) {
				cleanupCalled = true
				return nil, nil
			},
		)
	})
	if runErr != nil {
		t.Fatalf("runDeaconCleanupOrphansWith() error = %v", runErr)
	}
	if cleanupCalled {
		t.Fatal("dry-run invoked destructive cleanup")
	}

	for _, want := range []string{
		"PID=4242",
		"PPID=101",
		"user=test-user",
		"town=/tmp/test-town",
		"protected_by=none",
		"decision=eligible: orphan cleanup candidate",
		"zero signals sent",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
}

func TestCleanupOrphansHelpDocumentsSafetyFilters(t *testing.T) {
	help := strings.ToLower(deaconCleanupOrphansCmd.Long)
	for _, want := range []string{"runtime name", "controlling tty", "tmux", "acp", "ide extension", "60 seconds", "gas town workspace"} {
		if !strings.Contains(help, want) {
			t.Errorf("cleanup-orphans help missing %q", want)
		}
	}
	if deaconCleanupOrphansCmd.Flags().Lookup("dry-run") == nil {
		t.Fatal("cleanup-orphans is missing --dry-run")
	}
}
