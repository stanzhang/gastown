package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
)

var (
	agentsResolveRole  string
	agentsResolveRig   string
	agentsResolveJSON  bool
	agentsResolveQuiet bool
)

var agentsResolveCmd = &cobra.Command{
	Use:   "resolve",
	Short: "Resolve the active agent bead for a role",
	Long: `Resolve the active agent bead for a role.

Agent identity beads are town-owned even when their IDs carry a rig prefix.
The resolver searches the authoritative town registry across durable issues
and ephemeral wisps, returning the same record from town, rig root, witness,
and refinery working directories. It falls back to the local database only
outside a Gas Town workspace. Closed beads are ignored.`,
	RunE: runAgentsResolve,
}

func init() {
	agentsResolveCmd.Flags().StringVar(&agentsResolveRole, "role", "", "Agent role to resolve (witness, refinery, crew, polecat, mayor, deacon)")
	agentsResolveCmd.Flags().StringVar(&agentsResolveRig, "rig", "", "Rig name for rig-scoped roles")
	agentsResolveCmd.Flags().BoolVar(&agentsResolveJSON, "json", false, "Output match provenance as JSON")
	agentsResolveCmd.Flags().BoolVar(&agentsResolveQuiet, "quiet", false, "Suppress no-match diagnostics")
	agentsCmd.AddCommand(agentsResolveCmd)
}

type agentBeadSource string

const (
	agentSourceRigWisps   agentBeadSource = "rig-wisps"
	agentSourceRigIssues  agentBeadSource = "rig-issues"
	agentSourceTownWisps  agentBeadSource = "town-wisps"
	agentSourceTownIssues agentBeadSource = "town-issues"
)

type agentBeadCandidate struct {
	ID       string
	Source   agentBeadSource
	BeadsDir string
	Status   string
	Issue    *beads.Issue
}

type agentsResolveResult struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	BeadsDir string `json:"beads_dir"`
	Status   string `json:"status"`
}

type agentBeadResolveErrorKind string

const (
	agentBeadNotFound  agentBeadResolveErrorKind = "agent_bead_not_found"
	agentBeadAmbiguous agentBeadResolveErrorKind = "agent_bead_ambiguous"
)

// agentBeadResolveError is returned when the town agent registry cannot yield
// exactly one active bead for a role. Callers can use errors.As instead of
// parsing diagnostics, while --json exposes Kind as error_type.
type agentBeadResolveError struct {
	Kind       agentBeadResolveErrorKind
	Role       string
	Rig        string
	Candidates []string
}

func (e *agentBeadResolveError) Error() string {
	switch e.Kind {
	case agentBeadAmbiguous:
		return fmt.Sprintf("multiple matching agent beads found for role %q in rig %q: %s", e.Role, e.Rig, strings.Join(e.Candidates, ", "))
	default:
		message := fmt.Sprintf("no agent bead found for role %q", e.Role)
		if e.Rig != "" {
			message += fmt.Sprintf(" in rig %q", e.Rig)
		}
		return message
	}
}

type agentsResolveErrorResult struct {
	Error      string   `json:"error"`
	ErrorType  string   `json:"error_type"`
	Role       string   `json:"role"`
	Rig        string   `json:"rig,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
}

func runAgentsResolve(cmd *cobra.Command, _ []string) error {
	role := strings.TrimSpace(agentsResolveRole)
	rig := strings.TrimSpace(agentsResolveRig)
	if role == "" {
		return fmt.Errorf("--role is required")
	}

	registryBeadsDir, err := resolveAgentTrackingBeadsDir()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	candidates, err := findAgentBeadCandidates(cwd, registryBeadsDir)
	if err != nil {
		return err
	}

	var matches []agentBeadCandidate
	for _, candidate := range candidates {
		if agentBeadMatches(candidate.Issue, role, rig) {
			matches = append(matches, candidate)
		}
	}

	match, err := pickBestAgentBead(matches)
	if err != nil {
		var resolveErr *agentBeadResolveError
		if errors.As(err, &resolveErr) {
			resolveErr.Role = role
			resolveErr.Rig = rig
		}
		return outputAgentBeadResolveError(cmd, err)
	}
	if match == nil {
		return outputAgentBeadResolveError(cmd, &agentBeadResolveError{Kind: agentBeadNotFound, Role: role, Rig: rig})
	}

	if agentsResolveJSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(agentsResolveResult{
			ID:       match.ID,
			Source:   string(match.Source),
			BeadsDir: match.BeadsDir,
			Status:   match.Status,
		})
	}

	fmt.Fprintln(cmd.OutOrStdout(), match.ID)
	return nil
}

func outputAgentBeadResolveError(cmd *cobra.Command, err error) error {
	var resolveErr *agentBeadResolveError
	if agentsResolveJSON && errors.As(err, &resolveErr) {
		_ = json.NewEncoder(cmd.OutOrStdout()).Encode(agentsResolveErrorResult{
			Error:      resolveErr.Error(),
			ErrorType:  string(resolveErr.Kind),
			Role:       resolveErr.Role,
			Rig:        resolveErr.Rig,
			Candidates: resolveErr.Candidates,
		})
		return NewSilentExit(1)
	}
	if agentsResolveQuiet && errors.As(err, &resolveErr) && resolveErr.Kind == agentBeadNotFound {
		return NewSilentExit(1)
	}
	return err
}

func findAgentBeadCandidates(cwd, registryBeadsDir string) ([]agentBeadCandidate, error) {
	issueSource, wispSource := agentSourceTownIssues, agentSourceTownWisps
	if beads.FindTownRoot(cwd) == "" {
		issueSource, wispSource = agentSourceRigIssues, agentSourceRigWisps
	}
	return loadAgentBeadsFromDir(registryBeadsDir, issueSource, wispSource)
}

func loadAgentBeadsFromDir(beadsDir string, issueSource, wispSource agentBeadSource) ([]agentBeadCandidate, error) {
	db := beads.NewWithBeadsDir(filepath.Dir(beadsDir), beadsDir)
	var candidates []agentBeadCandidate

	issues, err := listAgentIssues(db)
	if err != nil {
		return nil, fmt.Errorf("listing agent issues in %s: %w", beadsDir, err)
	}
	for _, issue := range issues {
		candidates = append(candidates, agentBeadCandidate{
			ID:       issue.ID,
			Source:   issueSource,
			BeadsDir: beadsDir,
			Status:   issue.Status,
			Issue:    issue,
		})
	}

	if wisps, err := db.List(beads.ListOptions{Ephemeral: true, Label: "gt:agent", Status: "all"}); err == nil {
		for _, wisp := range wisps {
			candidates = append(candidates, agentBeadCandidate{
				ID:       wisp.ID,
				Source:   wispSource,
				BeadsDir: beadsDir,
				Status:   wisp.Status,
				Issue:    wisp,
			})
		}
	}

	return candidates, nil
}

func listAgentIssues(db *beads.Beads) ([]*beads.Issue, error) {
	out, err := db.Run("list", "--label=gt:agent", "--include-infra", "--status=all", "--json", "--flat", "--no-pager", "--limit=0")
	if err != nil {
		return nil, err
	}
	if len(out) == 0 || !json.Valid(out) {
		return nil, nil
	}

	var issues []*beads.Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}
	return issues, nil
}

func agentBeadMatches(issue *beads.Issue, role, rig string) bool {
	if issue == nil {
		return false
	}

	fields := beads.ParseAgentFields(issue.Description)
	if fields.RoleType == role {
		if rig == "" || fields.Rig == rig {
			return true
		}
	}

	idRig, idRole, _, ok := beads.ParseAgentBeadID(issue.ID)
	if !ok || idRole != role {
		return false
	}
	if rig == "" {
		return idRig == ""
	}
	return idRig == rig
}

func pickBestAgentBead(candidates []agentBeadCandidate) (*agentBeadCandidate, error) {
	open := candidates[:0]
	for _, candidate := range candidates {
		if strings.EqualFold(candidate.Status, "closed") {
			continue
		}
		open = append(open, candidate)
	}
	if len(open) == 0 {
		return nil, nil
	}

	sort.Slice(open, func(i, j int) bool {
		leftRank := agentBeadSourceRank(open[i].Source)
		rightRank := agentBeadSourceRank(open[j].Source)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return open[i].ID < open[j].ID
	})

	bestRank := agentBeadSourceRank(open[0].Source)
	var sameRank []string
	for _, candidate := range open {
		if agentBeadSourceRank(candidate.Source) != bestRank {
			break
		}
		sameRank = append(sameRank, candidate.ID)
	}
	if len(sameRank) > 1 {
		return nil, &agentBeadResolveError{Kind: agentBeadAmbiguous, Candidates: sameRank}
	}

	return &open[0], nil
}

func agentBeadSourceRank(source agentBeadSource) int {
	switch source {
	case agentSourceTownWisps:
		return 0
	case agentSourceTownIssues:
		return 1
	case agentSourceRigWisps:
		return 2
	case agentSourceRigIssues:
		return 3
	default:
		return 99
	}
}
