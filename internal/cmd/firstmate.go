package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

const (
	firstmateSendRelativePath     = "bin/fm-send.sh"
	maxFirstmateTitleBytes        = 512
	maxFirstmateDescriptionBytes  = 10 * 1024
	maxFirstmateInstructionsBytes = 3 * 1024
	maxFirstmateDelegationBytes   = 16 * 1024
	firstmateTruncationMarker     = "\n[truncated by gt firstmate delegate]"
)

type firstmateDelegateOptions struct {
	root         string
	home         string
	instructions string
	dryRun       bool
}

type firstmateBead struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type firstmateDelegateDeps struct {
	showBead      func(context.Context, string) ([]byte, error)
	verifyTracked func(context.Context, string, string) error
	runSend       func(context.Context, string, string, string, string, string, io.Writer, io.Writer) error
	lookupEnv     func(string) (string, bool)
}

func init() {
	rootCmd.AddCommand(newFirstmateCommand(defaultFirstmateDelegateDeps()))
}

func newFirstmateCommand(deps firstmateDelegateDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "firstmate",
		GroupID: GroupWork,
		Short:   "Delegate beads through a FirstMate second mate",
		Long: `Bridge Gas Town work to FirstMate's durable second-mate transport.

The bridge only sends a request. It does not implement SSH, copy credentials,
close the bead, or claim that the remote work completed.`,
		RunE: requireSubcommand,
	}

	var opts firstmateDelegateOptions
	delegateCmd := &cobra.Command{
		Use:   "delegate BEAD_ID SECONDMATE_ID",
		Short: "Send a bead to a FirstMate second mate",
		Long: `Resolve a bead as structured data and send a bounded request through the
tracked bin/fm-send.sh in a FirstMate checkout.

FirstMate exit 0 confirms durable request delivery only. Any nonzero exit,
including SSH exit 255 (unknown delivery), is returned unchanged with
fm-send's recovery instructions and stderr preserved exactly.

The FirstMate root is resolved from --firstmate-root, FIRSTMATE_ROOT, or
FM_ROOT_OVERRIDE. The home is resolved from --firstmate-home, FM_HOME, or the
resolved root. Use --dry-run to inspect the target and complete payload without
executing fm-send.

Examples:
  gt firstmate delegate gt-abc alienware-ml \
    --firstmate-root /Users/me/github/firstmate --dry-run
  gt firstmate delegate gt-abc alienware-ml \
    --firstmate-root /Users/me/github/firstmate \
    --instructions "Run focused tests and report evidence back through FirstMate"`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			err := runFirstmateDelegate(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], args[1], opts, deps)
			if _, silent := IsSilentExit(err); silent {
				// fm-send already emitted its authoritative stderr. Prevent Cobra from
				// appending "Error: exit N" or usage text to the recovery contract.
				cmd.SilenceErrors = true
				cmd.SilenceUsage = true
			}
			return err
		},
	}
	delegateCmd.Flags().StringVar(&opts.root, "firstmate-root", "", "FirstMate code checkout containing tracked bin/fm-send.sh (env: FIRSTMATE_ROOT or FM_ROOT_OVERRIDE)")
	delegateCmd.Flags().StringVar(&opts.home, "firstmate-home", "", "FirstMate home whose second-mate route is used (env: FM_HOME; default: resolved root)")
	delegateCmd.Flags().StringVar(&opts.instructions, "instructions", "", "Optional operator instructions appended to the bounded request")
	delegateCmd.Flags().BoolVarP(&opts.dryRun, "dry-run", "n", false, "Print the resolved target and payload without executing fm-send")
	cmd.AddCommand(delegateCmd)
	return cmd
}

func defaultFirstmateDelegateDeps() firstmateDelegateDeps {
	return firstmateDelegateDeps{
		showBead: func(_ context.Context, beadID string) ([]byte, error) {
			bdc := BdCmd("show", beadID, "--json").StripBeadsDir()
			if dir := resolveBeadDir(beadID); dir != "" && dir != "." {
				bdc.Dir(dir)
			}
			return bdc.Output()
		},
		verifyTracked: verifyFirstmateSendTracked,
		runSend:       executeFirstmateSend,
		lookupEnv:     os.LookupEnv,
	}
}

func runFirstmateDelegate(ctx context.Context, stdout, stderr io.Writer, beadID, secondmateID string, opts firstmateDelegateOptions, deps firstmateDelegateDeps) error {
	root, home, script, err := resolveFirstmatePaths(ctx, opts, deps)
	if err != nil {
		return err
	}
	target, err := normalizeFirstmateTarget(secondmateID)
	if err != nil {
		return err
	}

	beadJSON, err := deps.showBead(ctx, beadID)
	if err != nil {
		return fmt.Errorf("resolving bead %s with bd show --json: %w", beadID, err)
	}
	bead, err := parseFirstmateBead(beadID, beadJSON)
	if err != nil {
		return err
	}
	payload := buildFirstmateDelegationRequest(bead, opts.instructions)

	if opts.dryRun {
		fmt.Fprintln(stdout, "FirstMate delegation dry run (no request sent)")
		fmt.Fprintf(stdout, "root: %s\n", root)
		fmt.Fprintf(stdout, "home: %s\n", home)
		fmt.Fprintf(stdout, "target: %s\n", target)
		fmt.Fprintf(stdout, "payload-bytes: %d\n", len(payload))
		fmt.Fprintln(stdout, "--- payload begin ---")
		fmt.Fprintln(stdout, payload)
		fmt.Fprintln(stdout, "--- payload end ---")
		return nil
	}

	if err := deps.runSend(ctx, script, root, home, target, payload, stdout, stderr); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "FirstMate durably delivered the request to %s; bead %s remains open and remote completion is not claimed.\n", target, bead.ID)
	return nil
}

func resolveFirstmatePaths(ctx context.Context, opts firstmateDelegateOptions, deps firstmateDelegateDeps) (root, home, script string, err error) {
	root = strings.TrimSpace(opts.root)
	if root == "" {
		root = firstNonemptyEnv(deps.lookupEnv, "FIRSTMATE_ROOT", "FM_ROOT_OVERRIDE")
	}
	if root == "" {
		return "", "", "", fmt.Errorf("FirstMate root is required: pass --firstmate-root or set FIRSTMATE_ROOT or FM_ROOT_OVERRIDE")
	}
	root, err = canonicalDirectory(root, "FirstMate root")
	if err != nil {
		return "", "", "", err
	}

	home = strings.TrimSpace(opts.home)
	if home == "" {
		home = firstNonemptyEnv(deps.lookupEnv, "FM_HOME")
	}
	if home == "" {
		home = root
	}
	home, err = canonicalDirectory(home, "FirstMate home")
	if err != nil {
		return "", "", "", err
	}

	script = filepath.Join(root, filepath.FromSlash(firstmateSendRelativePath))
	info, statErr := os.Stat(script)
	if statErr != nil {
		return "", "", "", fmt.Errorf("finding FirstMate send script %s: %w", script, statErr)
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", "", "", fmt.Errorf("FirstMate send script is not an executable regular file: %s", script)
	}
	if err := deps.verifyTracked(ctx, root, firstmateSendRelativePath); err != nil {
		return "", "", "", err
	}
	return root, home, script, nil
}

func canonicalDirectory(path, label string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving %s %q: %w", label, path, err)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolving %s %q: %w", label, path, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("reading %s %q: %w", label, canonical, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory: %s", label, canonical)
	}
	return canonical, nil
}

func firstNonemptyEnv(lookup func(string) (string, bool), keys ...string) string {
	for _, key := range keys {
		if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func verifyFirstmateSendTracked(ctx context.Context, root, relativePath string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "--error-unmatch", "--", filepath.ToSlash(relativePath))
	if output, err := cmd.CombinedOutput(); err != nil {
		detail := strings.TrimSpace(string(output))
		if detail != "" {
			return fmt.Errorf("FirstMate send script is not tracked by the checkout at %s: %s", root, detail)
		}
		return fmt.Errorf("FirstMate send script is not tracked by the checkout at %s: %w", root, err)
	}
	return nil
}

func normalizeFirstmateTarget(secondmateID string) (string, error) {
	id := strings.TrimSpace(secondmateID)
	id = strings.TrimPrefix(id, "fm-")
	if id == "" {
		return "", fmt.Errorf("second-mate ID is required")
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return "", fmt.Errorf("invalid second-mate ID %q: use letters, digits, dot, underscore, or hyphen", secondmateID)
	}
	return "fm-" + id, nil
}

func parseFirstmateBead(requestedID string, data []byte) (firstmateBead, error) {
	var beads []firstmateBead
	if err := json.Unmarshal(data, &beads); err != nil {
		return firstmateBead{}, fmt.Errorf("parsing bd show --json for %s: %w", requestedID, err)
	}
	if len(beads) != 1 {
		return firstmateBead{}, fmt.Errorf("bd show --json for %s returned %d records; expected exactly one", requestedID, len(beads))
	}
	bead := beads[0]
	if bead.ID != requestedID {
		return firstmateBead{}, fmt.Errorf("bd show --json returned bead %q while %q was requested", bead.ID, requestedID)
	}
	if strings.TrimSpace(bead.Title) == "" {
		return firstmateBead{}, fmt.Errorf("bead %s has an empty title", requestedID)
	}
	return bead, nil
}

func buildFirstmateDelegationRequest(bead firstmateBead, instructions string) string {
	title := truncateFirstmateField(strings.TrimSpace(bead.Title), maxFirstmateTitleBytes)
	description := truncateFirstmateField(strings.TrimSpace(bead.Description), maxFirstmateDescriptionBytes)
	if description == "" {
		description = "(none provided)"
	}

	var b strings.Builder
	fmt.Fprintln(&b, "Gas Town remote delegation request")
	fmt.Fprintf(&b, "Bead ID: %s\n", bead.ID)
	fmt.Fprintf(&b, "Title: %s\n", title)
	fmt.Fprintln(&b, "Description:")
	fmt.Fprintln(&b, description)
	if trimmed := strings.TrimSpace(instructions); trimmed != "" {
		fmt.Fprintln(&b, "Operator instructions:")
		fmt.Fprintln(&b, truncateFirstmateField(trimmed, maxFirstmateInstructionsBytes))
	}
	fmt.Fprintln(&b, "Delivery contract: work is delegated for remote execution; do not treat request delivery as bead closure or proof of remote completion. Report progress and terminal evidence back through FirstMate using the bead ID.")

	payload := strings.TrimSuffix(b.String(), "\n")
	if len(payload) > maxFirstmateDelegationBytes {
		payload = truncateFirstmateField(payload, maxFirstmateDelegationBytes)
	}
	return payload
}

func truncateFirstmateField(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	limit := maxBytes - len(firstmateTruncationMarker)
	if limit < 0 {
		limit = 0
	}
	if limit < len(value) {
		for limit > 0 && !utf8.RuneStart(value[limit]) {
			limit--
		}
	}
	return value[:limit] + firstmateTruncationMarker
}

func executeFirstmateSend(ctx context.Context, script, root, home, target, payload string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, script, target, payload)
	cmd.Env = replaceEnvironment(os.Environ(), map[string]string{
		"FM_HOME":          home,
		"FM_ROOT_OVERRIDE": root,
	})
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return NewSilentExit(exitErr.ExitCode())
		}
		return fmt.Errorf("executing %s: %w", script, err)
	}
	return nil
}

func replaceEnvironment(base []string, replacements map[string]string) []string {
	out := make([]string, 0, len(base)+len(replacements))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, replaced := replacements[key]; replaced {
				continue
			}
		}
		out = append(out, entry)
	}
	for _, key := range []string{"FM_HOME", "FM_ROOT_OVERRIDE"} {
		out = append(out, key+"="+replacements[key])
	}
	return out
}
