//go:build windows

package util

// OrphanedProcess represents a claude process running without a controlling terminal.
// On Windows, orphan cleanup is not supported, so this is a stub definition.
type OrphanedProcess struct {
	PID      int
	Cmd      string
	Age      int    // Age in seconds
	TownRoot string // Gas Town workspace root, or "" if not in any workspace
}

// OrphanProcessAssessment records the evidence used to decide whether an agent
// process is safe to clean up. Orphan cleanup is unsupported on Windows.
type OrphanProcessAssessment struct {
	Process     OrphanedProcess
	ParentPID   int
	User        string
	TTY         string
	ProtectedBy string
	Decision    string
	Eligible    bool
}

// CleanupResult describes what happened to an orphaned process.
// On Windows, cleanup is a no-op.
type CleanupResult struct {
	Process OrphanedProcess
	Signal  string // "SIGTERM", "SIGKILL", or "UNKILLABLE"
	Error   error
}

// ZombieProcess represents a claude process not in any active tmux session.
// On Windows, zombie cleanup is not supported, so this is a stub definition.
type ZombieProcess struct {
	PID      int
	Cmd      string
	Age      int    // Age in seconds
	TTY      string // TTY column from ps
	TownRoot string // Gas Town workspace root, or "" if not in any workspace
}

// ZombieCleanupResult describes what happened to a zombie process.
// On Windows, cleanup is a no-op.
type ZombieCleanupResult struct {
	Process ZombieProcess
	Signal  string // "SIGTERM", "SIGKILL", or "UNKILLABLE"
	Error   error
}

// FindOrphanedClaudeProcesses is a Windows stub.
func FindOrphanedClaudeProcesses() ([]OrphanedProcess, error) {
	return nil, nil
}

// AssessOrphanedClaudeProcesses is a Windows stub.
func AssessOrphanedClaudeProcesses() ([]OrphanProcessAssessment, error) {
	return nil, nil
}

// CleanupOrphanedClaudeProcesses is a Windows stub.
func CleanupOrphanedClaudeProcesses() ([]CleanupResult, error) {
	return nil, nil
}

// CleanupAssessedOrphanedClaudeProcesses is a Windows stub.
func CleanupAssessedOrphanedClaudeProcesses([]OrphanProcessAssessment) ([]CleanupResult, error) {
	return nil, nil
}

// FindZombieClaudeProcesses is a Windows stub.
func FindZombieClaudeProcesses() ([]ZombieProcess, error) {
	return nil, nil
}

// CleanupZombieClaudeProcesses is a Windows stub.
func CleanupZombieClaudeProcesses() ([]ZombieCleanupResult, error) {
	return nil, nil
}
