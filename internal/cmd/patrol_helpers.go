package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/cli"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/lock"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/style"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// PatrolConfig holds role-specific patrol configuration.
type PatrolConfig struct {
	RoleName      string       // "deacon", "witness", "refinery"
	PatrolMolName string       // "mol-deacon-patrol", etc.
	BeadsDir      string       // where to look for beads
	Assignee      string       // agent identity for pinning
	HeaderEmoji   string       // display emoji
	HeaderTitle   string       // "Patrol Status", etc.
	WorkLoopSteps []string     // role-specific instructions
	ExtraVars     []string     // additional --var key=value args for wisp creation
	Beads         *beads.Beads // optional injected beads instance (for test isolation)
}

// Bound legacy-root inspection so hundreds of historical naked patrols cannot
// turn startup into an unbounded sequence of Dolt child queries. A newly created
// complete patrol sorts first on the next lookup.
const maxPatrolCandidatesToInspect = 20

// findActivePatrol finds an active patrol molecule for the role.
// Returns the patrol ID, display line, and whether one was found.
// Returns an error if discovery fails (e.g. transient bd failure),
// so callers can distinguish "no patrol" from "discovery failed"
// and avoid auto-spawning duplicates.
//
// Patrol molecules are intentionally hooked to the agent (hooked status).
// This function looks up hooked patrols and distinguishes active ones
// (with open/in_progress children) from historical or partially-created roots.
// Discovery is read-only: historical patrol evidence is never burned merely
// because a newer cycle is being selected.
func findActivePatrol(cfg PatrolConfig) (patrolID, patrolLine string, found bool, err error) {
	b := cfg.Beads
	if b == nil {
		b = beads.New(cfg.BeadsDir)
	}

	// Find active patrol beads for this agent across durable issues and wisps.
	hookedBeads, listErr := listAssignedActiveWorkAcrossStatuses(b, cfg.Assignee)
	if listErr != nil {
		return "", "", false, fmt.Errorf("listing active patrol work: %w", listErr)
	}

	// Identify the newest executable patrol. listAssignedActiveWorkAcrossStatuses
	// is ordered by recency, which is also the authority used by hook lookup.
	var skipped int // tracks patrols skipped due to child-listing errors
	var inspected int

	for _, bead := range hookedBeads {
		if !strings.HasPrefix(bead.Title, cfg.PatrolMolName) {
			continue
		}
		if inspected >= maxPatrolCandidatesToInspect {
			break
		}
		inspected++

		hasOpen, err := checkHasOpenChildren(b, bead.ID)
		if err != nil {
			// Transient error — skip this bead entirely to avoid
			// destructive cleanup of a potentially active patrol.
			style.PrintWarning("could not check children for %s: %v", bead.ID, err)
			skipped++
			continue
		}

		if hasOpen {
			return bead.ID, formatBeadLine(bead), true, nil
		}
	}

	// If we found matching patrols but skipped them all due to errors,
	// return an error so the caller doesn't auto-spawn a duplicate.
	if skipped > 0 {
		return "", "", false, fmt.Errorf("discovery incomplete: %d patrol(s) skipped due to child-listing errors", skipped)
	}
	return "", "", false, nil
}

// checkHasOpenChildren returns true if the given parent has any children
// that are not in closed status (i.e., open or in_progress).
// Returns an error if the child listing fails, so the caller can avoid
// destructive cleanup on transient failures.
//
// A parent with zero children is not executable and therefore not active. Patrol
// creation does not hook a root until every formula step has materialized, so a
// zero-child hooked root is historical evidence of a partial/legacy creation and
// must not become the authoritative hook.
func checkHasOpenChildren(b *beads.Beads, parentID string) (bool, error) {
	children, err := listChildrenAcrossTables(b, parentID)
	if err != nil {
		return false, err
	}
	if len(children) == 0 {
		return false, nil
	}
	for _, child := range children {
		if child.Status != "closed" {
			return true, nil
		}
	}
	return false, nil
}

// formatBeadLine formats a bead issue into a display line similar to bd list output.
func formatBeadLine(issue *beads.Issue) string {
	return fmt.Sprintf("%s  %s [%s]", issue.ID, issue.Title, issue.Status)
}

// autoSpawnPatrol serializes patrol creation for a role. The lock makes the
// hooked bead selected by hook lookup the same successor returned to callers.
func autoSpawnPatrol(cfg PatrolConfig) (string, error) {
	if stop, err := refineryPatrolSafetyStop(cfg); err != nil {
		return "", err
	} else if stop != nil {
		return "", refinery.NewSafetyStoppedError(stop)
	}

	unlock, err := acquirePatrolCycleLock(cfg)
	if err != nil {
		return "", err
	}
	defer unlock()

	return autoSpawnPatrolLocked(cfg)
}

// autoSpawnPatrolLocked creates a fully materialized patrol, records formula
// metadata, and only then exposes it as hooked work. The caller must hold the
// role's patrol-cycle lock.
func autoSpawnPatrolLocked(cfg PatrolConfig) (string, error) {
	if stop, err := refineryPatrolSafetyStop(cfg); err != nil {
		return "", err
	} else if stop != nil {
		return "", refinery.NewSafetyStoppedError(stop)
	}
	if patrolID, _, found, err := findActivePatrol(cfg); err != nil {
		return "", fmt.Errorf("checking existing patrol before create: %w", err)
	} else if found {
		return patrolID, nil
	}

	// Resolve the beads directory following redirects.
	// This ensures bd targets the correct database (e.g., rig database
	// instead of HQ) regardless of inherited BEADS_DIR. See gt-ctir.
	resolvedBeadsDir := beads.ResolveBeadsDir(cfg.BeadsDir)

	// Find the proto ID for the patrol molecule
	cmdCatalog := exec.Command("gt", "formula", "list")
	cmdCatalog.Dir = cfg.BeadsDir
	var stdoutCatalog, stderrCatalog bytes.Buffer
	cmdCatalog.Stdout = &stdoutCatalog
	cmdCatalog.Stderr = &stderrCatalog

	if err := cmdCatalog.Run(); err != nil {
		errMsg := strings.TrimSpace(stderrCatalog.String())
		if errMsg != "" {
			return "", fmt.Errorf("failed to list formulas: %s", errMsg)
		}
		return "", fmt.Errorf("failed to list formulas: %w", err)
	}

	// Find patrol molecule in formula list
	// Format: "formula-name         description"
	var protoID string
	catalogLines := strings.Split(stdoutCatalog.String(), "\n")
	for _, line := range catalogLines {
		if strings.Contains(line, cfg.PatrolMolName) {
			parts := strings.Fields(line)
			if len(parts) > 0 {
				protoID = parts[0]
				break
			}
		}
	}

	if protoID == "" {
		return "", fmt.Errorf("proto %s not found in catalog", cfg.PatrolMolName)
	}

	// Materialize the entire formula. A patrol root without child steps is not
	// executable by bd mol current/progress and must never be hooked.
	formulaVars := patrolFormulaVars(cfg)
	spawnArgs := patrolSpawnArgs(protoID, cfg.RoleName, formulaVars)
	cmdSpawn := BdCmd(spawnArgs...).
		WithAutoCommit().
		WithBeadsDir(resolvedBeadsDir).
		Dir(cfg.BeadsDir).
		Build()
	var stdoutSpawn, stderrSpawn bytes.Buffer
	cmdSpawn.Stdout = &stdoutSpawn
	cmdSpawn.Stderr = &stderrSpawn

	spawnErr := cmdSpawn.Run()
	patrolID := parsePatrolWispID(stdoutSpawn.String())
	if spawnErr != nil {
		if patrolID != "" {
			_ = failClosedPatrol(cfg, patrolID, "patrol creation failed")
		}
		detail := strings.TrimSpace(stderrSpawn.String())
		if detail == "" {
			detail = spawnErr.Error()
		}
		return "", fmt.Errorf("failed to create patrol wisp: %s", detail)
	}

	if patrolID == "" {
		return "", fmt.Errorf("created wisp but could not parse ID from output")
	}

	if err := validatePatrolMaterialization(cfg, patrolID); err != nil {
		_ = failClosedPatrol(cfg, patrolID, "incomplete patrol materialization")
		return "", err
	}

	desc, err := buildPatrolWispDescription(cfg, time.Now().UTC())
	if err != nil {
		_ = failClosedPatrol(cfg, patrolID, "patrol metadata render failed")
		return "", fmt.Errorf("rendering patrol metadata for %s: %w", patrolID, err)
	}
	if err := updatePatrolWispDescription(cfg, resolvedBeadsDir, patrolID, desc); err != nil {
		_ = failClosedPatrol(cfg, patrolID, "patrol metadata write failed")
		return "", fmt.Errorf("writing patrol metadata for %s: %w", patrolID, err)
	}

	// Hook last. hookBeadWithRetry verifies the authoritative status+assignee
	// record that gt hook/mol status read.
	hookDir := beads.ResolveHookDir(cfg.BeadsDir, patrolID, resolvedBeadsDir)
	if err := hookBeadWithRetryFn(patrolID, cfg.Assignee, hookDir); err != nil {
		_ = failClosedPatrol(cfg, patrolID, "patrol hook failed")
		return "", fmt.Errorf("hooking patrol %s: %w", patrolID, err)
	}

	resolvedID, _, found, err := findActivePatrol(cfg)
	if err != nil || !found || resolvedID != patrolID {
		_ = failClosedPatrol(cfg, patrolID, "patrol successor verification failed")
		if err != nil {
			return "", fmt.Errorf("verifying patrol successor %s: %w", patrolID, err)
		}
		return "", fmt.Errorf("verifying patrol successor: created %s but hook resolves %s", patrolID, resolvedID)
	}

	return patrolID, nil
}

func patrolSpawnArgs(protoID, actor string, formulaVars []string) []string {
	args := []string{"mol", "wisp", "create", protoID, "--actor", actor}
	for _, variable := range formulaVars {
		args = append(args, "--var", variable)
	}
	return args
}

func acquirePatrolCycleLock(cfg PatrolConfig) (func(), error) {
	locksDir := filepath.Join(beads.ResolveBeadsDir(cfg.BeadsDir), "locks")
	if err := os.MkdirAll(locksDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating patrol lock directory: %w", err)
	}
	name := strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(cfg.Assignee)
	unlock, err := lock.FlockAcquire(filepath.Join(locksDir, "patrol-"+name+".flock"))
	if err != nil {
		return nil, fmt.Errorf("acquiring patrol cycle lock for %s: %w", cfg.Assignee, err)
	}
	return unlock, nil
}

func parsePatrolWispID(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Root issue:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Root issue:"))
		}
	}
	for _, p := range strings.Fields(output) {
		if strings.Contains(p, "-wisp-") {
			return strings.Trim(p, "\"',")
		}
	}
	return ""
}

func validatePatrolMaterialization(cfg PatrolConfig, patrolID string) error {
	f, varMap, err := resolveFormulaForRendering(cfg.PatrolMolName, cfg.BeadsDir, patrolRigName(cfg), patrolFormulaVars(cfg))
	if err != nil {
		return fmt.Errorf("validating patrol %s: %w", patrolID, err)
	}
	b := cfg.Beads
	if b == nil {
		b = beads.New(cfg.BeadsDir)
	}
	children, err := listChildrenAcrossTables(b, patrolID)
	if err != nil {
		return fmt.Errorf("validating patrol %s steps: %w", patrolID, err)
	}
	expected := make(map[string]struct{})
	for _, stepID := range f.GetAllIDs() {
		expected[stepID] = struct{}{}
	}
	expectedTitles := make(map[string]int, len(f.Steps))
	expectedTitleByID := make(map[string]string, len(f.Steps))
	for _, step := range f.Steps {
		title := applyFormulaVars(step.Title, varMap)
		expectedTitles[title]++
		expectedTitleByID[step.ID] = title
	}
	if len(children) != len(expected) {
		return fmt.Errorf("patrol %s materialized %d/%d formula steps", patrolID, len(children), len(expected))
	}
	seen := make(map[string]struct{}, len(children))
	for _, child := range children {
		stepID := materializedStepID(child.Description)
		if _, ok := expected[stepID]; ok {
			if _, duplicate := seen[stepID]; duplicate {
				return fmt.Errorf("patrol %s materialized duplicate step %q", patrolID, stepID)
			}
			seen[stepID] = struct{}{}
			expectedTitles[expectedTitleByID[stepID]]--
			continue
		}
		// Cooked protos store the template bead ID in template_step rather
		// than the formula's logical step ID. In that format, the copied title
		// is the stable identity available to the instantiated child.
		if expectedTitles[child.Title] == 0 {
			return fmt.Errorf("patrol %s has unexpected or unidentified step %s (%q, title %q)", patrolID, child.ID, stepID, child.Title)
		}
		expectedTitles[child.Title]--
	}
	return nil
}

func materializedStepID(description string) string {
	for _, line := range strings.Split(description, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "template_step", "step", "step_id":
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func failClosedPatrol(cfg PatrolConfig, patrolID, reason string) error {
	b := cfg.Beads
	if b == nil {
		b = beads.New(cfg.BeadsDir)
	}
	_, descendantsErr := forceCloseDescendants(b, patrolID)
	rootErr := b.ForceCloseWithReason(reason, patrolID)
	return errors.Join(descendantsErr, rootErr)
}

func patrolFormulaVars(cfg PatrolConfig) []string {
	rigName := patrolRigName(cfg)
	ctx := RoleContext{TownRoot: cfg.BeadsDir, Rig: rigName}
	var vars []string
	switch cfg.PatrolMolName {
	case constants.MolWitnessPatrol:
		vars = buildWitnessPatrolVars(ctx)
	case constants.MolRefineryPatrol:
		vars = buildRefineryPatrolVars(ctx)
	}
	for _, override := range cfg.ExtraVars {
		key, _, ok := strings.Cut(override, "=")
		if !ok {
			vars = append(vars, override)
			continue
		}
		replaced := false
		for i, existing := range vars {
			existingKey, _, _ := strings.Cut(existing, "=")
			if existingKey == key {
				vars[i] = override
				replaced = true
			}
		}
		if !replaced {
			vars = append(vars, override)
		}
	}
	return vars
}

func renderPatrolWispDescription(cfg PatrolConfig) (string, error) {
	rigName := patrolRigName(cfg)
	return renderFormulaRootAndStepsFull(cfg.PatrolMolName, cfg.BeadsDir, rigName, patrolFormulaVars(cfg))
}

func buildPatrolWispDescription(cfg PatrolConfig, attachedAt time.Time) (string, error) {
	rendered, err := renderPatrolWispDescription(cfg)
	if err != nil {
		return "", err
	}
	formulaVars := patrolFormulaVars(cfg)
	return beads.SetAttachmentFields(&beads.Issue{
		Description: "wisp: true\ninstantiated_from: " + cfg.PatrolMolName + "\n\n" + rendered,
	}, &beads.AttachmentFields{
		AttachedFormula: cfg.PatrolMolName,
		AttachedAt:      attachedAt.UTC().Format(time.RFC3339Nano),
		AttachedVars:    append([]string(nil), formulaVars...),
		FormulaVars:     strings.Join(formulaVars, "\n"),
	}), nil
}

func patrolRigName(cfg PatrolConfig) string {
	rigName, _, ok := strings.Cut(cfg.Assignee, "/")
	if !ok {
		return ""
	}
	return rigName
}

func updatePatrolWispDescription(cfg PatrolConfig, resolvedBeadsDir, patrolID, desc string) error {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return nil
	}
	return BdCmd("update", patrolID, "--body-file=-").
		Stdin(strings.NewReader(desc)).
		WithAutoCommit().
		WithBeadsDir(resolvedBeadsDir).
		Dir(cfg.BeadsDir).
		Run()
}

// outputPatrolContext is the main function that handles patrol display logic.
// It finds or creates a patrol and outputs the status and work loop.
func outputPatrolContext(cfg PatrolConfig) {
	fmt.Println()
	fmt.Printf("%s\n\n", style.Bold.Render(fmt.Sprintf("## %s %s", cfg.HeaderEmoji, cfg.HeaderTitle)))

	// Try to find an active patrol
	patrolID, patrolLine, hasPatrol, findErr := findActivePatrol(cfg)

	if findErr != nil {
		// Discovery failed — do NOT auto-spawn to avoid creating duplicates
		style.PrintWarning("patrol discovery failed: %v", findErr)
		fmt.Println("Status: **Discovery failed** — cannot determine patrol state")
		fmt.Println(style.Dim.Render("Check bd connectivity and retry. Not spawning new patrol to avoid duplicates."))
		return
	}

	if !hasPatrol {
		// No active patrol - auto-spawn one
		fmt.Printf("Status: **No active patrol** - creating %s...\n", cfg.PatrolMolName)
		fmt.Println()

		var err error
		patrolID, err = autoSpawnPatrol(cfg)
		if err != nil {
			if errors.Is(err, refinery.ErrSafetyStopped) {
				fmt.Println(style.Dim.Render(err.Error()))
				return
			}
			if patrolID != "" {
				fmt.Printf("⚠ %s\n", err.Error())
			} else {
				fmt.Println(style.Dim.Render(err.Error()))
				fmt.Println(style.Dim.Render("Run `" + cli.Name() + " formula list` to troubleshoot."))
				return
			}
		} else {
			fmt.Printf("✓ Created and hooked patrol wisp: %s\n", patrolID)
		}
	} else {
		// Has active patrol - show status
		fmt.Println("Status: **Patrol Active**")
		fmt.Printf("Patrol: %s\n\n", strings.TrimSpace(patrolLine))
	}

	// Show patrol work loop instructions
	fmt.Printf("**%s Patrol Work Loop:**\n", cases.Title(language.English).String(cfg.RoleName))
	for i, step := range cfg.WorkLoopSteps {
		fmt.Printf("%d. %s\n", i+1, step)
	}

	if patrolID != "" {
		fmt.Println()
		fmt.Printf("Current patrol ID: %s\n", patrolID)
	}
}

func refineryPatrolSafetyStop(cfg PatrolConfig) (*refinery.SafetyStop, error) {
	if cfg.RoleName != "refinery" {
		return nil, nil
	}
	rigName := strings.TrimSuffix(cfg.Assignee, "/refinery")
	if rigName == cfg.Assignee || rigName == "" {
		return nil, nil
	}
	return refinery.ActiveSafetyStop(cfg.BeadsDir, rigName)
}
