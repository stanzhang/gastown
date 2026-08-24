package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRunFirstmateDelegateDryRunDoesNotSend(t *testing.T) {
	root := makeFakeFirstmateRoot(t)
	bead := []firstmateBead{{ID: "gt-abc", Title: "Fix the bridge", Description: "Preserve transport evidence."}}
	data, err := json.Marshal(bead)
	if err != nil {
		t.Fatal(err)
	}

	sendCalled := false
	deps := firstmateDelegateDeps{
		showBead: func(_ context.Context, gotID string) ([]byte, error) {
			if gotID != "gt-abc" {
				t.Fatalf("show bead ID = %q", gotID)
			}
			return data, nil
		},
		verifyTracked: func(context.Context, string, string) error { return nil },
		runSend: func(context.Context, string, string, string, string, string, io.Writer, io.Writer) error {
			sendCalled = true
			return nil
		},
		lookupEnv: func(string) (string, bool) { return "", false },
	}

	var stdout, stderr bytes.Buffer
	err = runFirstmateDelegate(context.Background(), &stdout, &stderr, "gt-abc", "alienware-ml", firstmateDelegateOptions{
		root:         root,
		home:         root,
		instructions: "Run focused tests.",
		dryRun:       true,
	}, deps)
	if err != nil {
		t.Fatalf("runFirstmateDelegate: %v", err)
	}
	if sendCalled {
		t.Fatal("dry-run executed fm-send")
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	for _, want := range []string{
		"FirstMate delegation dry run (no request sent)",
		"target: fm-alienware-ml",
		"Bead ID: gt-abc",
		"Title: Fix the bridge",
		"Preserve transport evidence.",
		"Operator instructions:\nRun focused tests.",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("dry-run output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestBuildFirstmateDelegationRequestIsBoundedAndUTF8(t *testing.T) {
	request := buildFirstmateDelegationRequest(firstmateBead{
		ID:          "gt-big",
		Title:       strings.Repeat("界", 1000),
		Description: strings.Repeat("é", 10000),
	}, strings.Repeat("instruction ", 1000))

	if len(request) > maxFirstmateDelegationBytes {
		t.Fatalf("request is %d bytes, max %d", len(request), maxFirstmateDelegationBytes)
	}
	if !utf8.ValidString(request) {
		t.Fatal("request truncation produced invalid UTF-8")
	}
	if !strings.Contains(request, "Bead ID: gt-big") || !strings.Contains(request, "[truncated by gt firstmate delegate]") {
		t.Fatalf("bounded request missing identity or truncation marker:\n%s", request)
	}
}

func TestExecuteFirstmateSendPreservesStderrAndExit255(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fm-send.sh is a POSIX executable")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fm-send.sh")
	sentinel := filepath.Join(dir, "must-not-exist")
	source := "#!/bin/sh\n" +
		"printf 'target=%s\\npayload=%s\\nhome=%s\\nroot=%s\\n' \"$1\" \"$2\" \"$FM_HOME\" \"$FM_ROOT_OVERRIDE\"\n" +
		"printf 'unknown delivery; FM_PENDING_REPLY_EXISTING_CORR=corr retry exactly\\n' >&2\n" +
		"exit 255\n"
	if err := os.WriteFile(script, []byte(source), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := "literal $(touch " + sentinel + ")"
	var stdout, stderr bytes.Buffer
	err := executeFirstmateSend(context.Background(), script, "/firstmate/root", "/firstmate/home", "fm-alienware-ml", payload, &stdout, &stderr)
	code, ok := IsSilentExit(err)
	if !ok || code != 255 {
		t.Fatalf("error = %T %v, want silent exit 255", err, err)
	}
	if got, want := stderr.String(), "unknown delivery; FM_PENDING_REPLY_EXISTING_CORR=corr retry exactly\n"; got != want {
		t.Fatalf("stderr = %q, want exact %q", got, want)
	}
	if !strings.Contains(stdout.String(), "payload="+payload) || !strings.Contains(stdout.String(), "home=/firstmate/home") {
		t.Fatalf("argv/environment not preserved:\n%s", stdout.String())
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("payload was interpreted as a shell string; sentinel stat error = %v", err)
	}
}

func TestFirstmateCommandDoesNotAppendCobraTextToFmSendError(t *testing.T) {
	root := makeFakeFirstmateRoot(t)
	data := []byte(`[{"id":"gt-abc","title":"Bridge","description":"Delegate it"}]`)
	deps := firstmateDelegateDeps{
		showBead:      func(context.Context, string) ([]byte, error) { return data, nil },
		verifyTracked: func(context.Context, string, string) error { return nil },
		runSend: func(_ context.Context, _, _, _, _, _ string, _ io.Writer, stderr io.Writer) error {
			fmt.Fprint(stderr, "exact recovery stderr\n")
			return NewSilentExit(255)
		},
		lookupEnv: func(string) (string, bool) { return "", false },
	}
	cmd := newFirstmateCommand(deps)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"delegate", "gt-abc", "alienware-ml", "--firstmate-root", root})
	err := cmd.Execute()
	if code, ok := IsSilentExit(err); !ok || code != 255 {
		t.Fatalf("error = %T %v, want silent exit 255", err, err)
	}
	if got, want := stderr.String(), "exact recovery stderr\n"; got != want {
		t.Fatalf("stderr = %q, want exact %q", got, want)
	}
}

func TestResolveFirstmatePathsUsesSafeEnvironmentPrecedence(t *testing.T) {
	root := makeFakeFirstmateRoot(t)
	home := t.TempDir()
	home, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"FIRSTMATE_ROOT":   root,
		"FM_ROOT_OVERRIDE": "/ignored/root",
		"FM_HOME":          home,
	}
	deps := firstmateDelegateDeps{
		lookupEnv: func(key string) (string, bool) {
			value, ok := env[key]
			return value, ok
		},
		verifyTracked: func(_ context.Context, gotRoot, gotPath string) error {
			if gotRoot != root || gotPath != firstmateSendRelativePath {
				return fmt.Errorf("unexpected tracked check: %s %s", gotRoot, gotPath)
			}
			return nil
		},
	}
	gotRoot, gotHome, script, err := resolveFirstmatePaths(context.Background(), firstmateDelegateOptions{}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != root || gotHome != home || script != filepath.Join(root, "bin", "fm-send.sh") {
		t.Fatalf("resolved root=%q home=%q script=%q", gotRoot, gotHome, script)
	}
}

func TestParseFirstmateBeadRejectsMismatchedStructuredResult(t *testing.T) {
	data := []byte(`[{"id":"gt-other","title":"Wrong bead","description":""}]`)
	_, err := parseFirstmateBead("gt-wanted", data)
	if err == nil || !strings.Contains(err.Error(), `returned bead "gt-other"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestNormalizeFirstmateTarget(t *testing.T) {
	for _, input := range []string{"alienware-ml", "fm-alienware-ml"} {
		got, err := normalizeFirstmateTarget(input)
		if err != nil || got != "fm-alienware-ml" {
			t.Fatalf("normalizeFirstmateTarget(%q) = %q, %v", input, got, err)
		}
	}
	if _, err := normalizeFirstmateTarget("bad/target"); err == nil {
		t.Fatal("invalid target accepted")
	}
}

func makeFakeFirstmateRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "fm-send.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
