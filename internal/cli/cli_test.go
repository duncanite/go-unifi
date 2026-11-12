package cli_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/forkcloser/xos"
	"github.com/mycophonic/primordium/filesystem"
	"github.com/mycophonic/primordium/network"

	"github.com/duncanite/go-unifi/console/consoletest"
	"github.com/duncanite/go-unifi/internal/cli"
)

// TestMain installs the process default transport app.Main installs, so
// the tests run against the same wrapped default the binary does.
func TestMain(m *testing.M) {
	network.SetDefaults()

	m.Run()
}

func house(t *testing.T) (*consoletest.Server, []string) {
	t.Helper()

	srv := consoletest.New(consoletest.House())
	t.Cleanup(srv.Close)

	sum := sha256.Sum256(srv.Certificate().Raw)

	return srv, []string{"--url", srv.URL, "--api-key", consoletest.APIKey, "--fingerprint", hex.EncodeToString(sum[:])}
}

func TestUsageAndMissingKey(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	if code := cli.Run(t.Context(), []string{"plan", t.TempDir()}, &stdout, &stderr); code != cli.ExitError {
		t.Fatalf("exit = %d without an API key, want %d", code, cli.ExitError)
	}

	if !strings.Contains(stderr.String(), "--api-key") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestDumpPlanApply(t *testing.T) {
	t.Parallel()

	srv, flags := house(t)
	dir := filepath.Join(t.TempDir(), "console")

	var stdout, stderr bytes.Buffer

	if code := cli.Run(t.Context(), append(flags, "dump", dir), &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("dump exit = %d: %s", code, stderr.String())
	}

	if !strings.Contains(stdout.String(), "networkconf: 2 documents") {
		t.Fatalf("dump said:\n%s", stdout.String())
	}

	if code := cli.Run(t.Context(), append(flags, "dump", dir), &stdout, &stderr); code != cli.ExitError {
		t.Fatal("a second dump into the same directory went through without --force")
	}

	stdout.Reset()

	if code := cli.Run(t.Context(), append(flags, "plan", dir), &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("plan exit = %d after dump: %s%s", code, stdout.String(), stderr.String())
	}

	path := filepath.Join(dir, "setting", "mgmt.yaml")

	data, err := xos.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := xos.WriteFile(
		path,
		bytes.Replace(data, []byte("led_enabled: false"), []byte("led_enabled: true"), 1),
		filesystem.FilePermissionsDefault,
	); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()

	if code := cli.Run(t.Context(), append(flags, "plan", dir), &stdout, &stderr); code != cli.ExitChanges {
		t.Fatalf("plan exit = %d with a drifting file, want %d: %s", code, cli.ExitChanges, stderr.String())
	}

	if !strings.Contains(stdout.String(), "~ led_enabled: false -> true") {
		t.Fatalf("plan said:\n%s", stdout.String())
	}

	stdout.Reset()

	if code := cli.Run(t.Context(), append(flags, "apply", dir), &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("apply exit = %d: %s", code, stderr.String())
	}

	if mgmt := srv.Setting("mgmt"); mgmt["led_enabled"] != true {
		t.Fatalf("the console was not updated: %v", mgmt)
	}

	stdout.Reset()

	if code := cli.Run(t.Context(), append(flags, "plan", dir), &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("plan exit = %d after apply: %s", code, stdout.String())
	}
}

// TestConcurrentAppliesCreateOnce: applies at once on one directory take
// turns, so the file's network is created by the first and found by the rest.
func TestConcurrentAppliesCreateOnce(t *testing.T) {
	t.Parallel()

	srv, flags := house(t)
	dir := filepath.Join(t.TempDir(), "console")

	var stdout, stderr bytes.Buffer

	if code := cli.Run(t.Context(), append(flags, "dump", dir), &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("dump exit = %d: %s", code, stderr.String())
	}

	if err := xos.WriteFile(
		filepath.Join(dir, "networkconf", "peoples.yaml"),
		[]byte("name: Peoples\npurpose: corporate\nvlan: 30\nvlan_enabled: true\n"),
		filesystem.FilePermissionsDefault,
	); err != nil {
		t.Fatal(err)
	}

	const applies = 4

	codes := make([]int, applies)
	outputs := make([]bytes.Buffer, applies)

	var group sync.WaitGroup

	for i := range applies {
		group.Go(func() {
			codes[i] = cli.Run(t.Context(), append(flags, "apply", dir), &outputs[i], &outputs[i])
		})
	}

	group.Wait()

	for i, code := range codes {
		if code != cli.ExitOK {
			t.Errorf("apply %d exit = %d: %s", i, code, outputs[i].String())
		}
	}

	var creates int

	for _, request := range srv.Requests {
		if request == "POST api/s/default/rest/networkconf" {
			creates++
		}
	}

	if creates != 1 {
		t.Fatalf("Peoples was created %d times; requests: %q", creates, srv.Requests)
	}
}

// TestForcedDumpKeepsTheComments: the notes a reader left in a file come
// through the dump that rewrites it, and the one on a key the console does
// not have is named.
func TestForcedDumpKeepsTheComments(t *testing.T) {
	t.Parallel()

	_, flags := house(t)
	dir := filepath.Join(t.TempDir(), "console")

	var stdout, stderr bytes.Buffer

	if code := cli.Run(t.Context(), append(flags, "dump", dir), &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("dump exit = %d: %s", code, stderr.String())
	}

	path := filepath.Join(dir, "setting", "mgmt.yaml")

	data, err := xos.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// A block set apart by a blank line sits on the document, not a key, and
	// so does a closing line; both go where they were.
	annotated := "# Site management.\n\n" +
		strings.Replace(string(data), "led_enabled: false\n", "led_enabled: false # the APs override it\n", 1) +
		"mistaken: 1 # not a key the console has\n\n# Reviewed in full.\n"
	if err := xos.WriteFile(path, []byte(annotated), filesystem.FilePermissionsDefault); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()

	if code := cli.Run(t.Context(), append(flags, "dump", dir, "--force"), &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("forced dump exit = %d: %s", code, stderr.String())
	}

	if !strings.Contains(stdout.String(), "comment left behind at "+path+": mistaken\n") {
		t.Fatalf("the orphan was not named:\n%s", stdout.String())
	}

	after, readErr := xos.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}

	if !strings.HasPrefix(string(after), "# Site management.\n\nkey: mgmt\n") ||
		!strings.Contains(string(after), "led_enabled: false # the APs override it\n") ||
		!strings.HasSuffix(string(after), "\n# Reviewed in full.\n") ||
		strings.Contains(string(after), "mistaken") {
		t.Fatalf("after the forced dump:\n%s", after)
	}
}

// TestDumpThatFailsLeavesNothing: the console is read before the directory
// is made, so a wrong key or a dead console leaves no empty directory.
func TestDumpThatFailsLeavesNothing(t *testing.T) {
	t.Parallel()

	_, flags := house(t)
	dir := filepath.Join(t.TempDir(), "consoel")

	var stdout, stderr bytes.Buffer

	flags[3] = "wrong-key"

	if code := cli.Run(t.Context(), append(flags, "dump", dir), &stdout, &stderr); code != cli.ExitError {
		t.Fatalf("dump exit = %d with a wrong key, want %d", code, cli.ExitError)
	}

	if _, err := xos.Stat(dir); err == nil {
		t.Fatal("the directory was created although nothing was dumped")
	}
}

func TestTLSPinningRefusesAnotherCertificate(t *testing.T) {
	t.Parallel()

	srv, _ := house(t)

	var stdout, stderr bytes.Buffer

	wrong := strings.Repeat("ab", sha256.Size)

	code := cli.Run(
		t.Context(),
		[]string{"--url", srv.URL, "--api-key", consoletest.APIKey, "--fingerprint", wrong, "plan", t.TempDir()},
		&stdout,
		&stderr,
	)
	if code != cli.ExitError || !strings.Contains(stderr.String(), "pinned fingerprint") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}

	stderr.Reset()

	code = cli.Run(
		t.Context(),
		[]string{"--url", srv.URL, "--api-key", consoletest.APIKey, "--fingerprint", "zz", "plan", t.TempDir()},
		&stdout,
		&stderr,
	)
	if code != cli.ExitError || !strings.Contains(stderr.String(), "not a hex SHA-256") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}

	stderr.Reset()

	code = cli.Run(
		t.Context(),
		[]string{"--url", srv.URL, "--api-key", consoletest.APIKey, "--insecure", "plan", t.TempDir()},
		&stdout,
		&stderr,
	)
	if code != cli.ExitOK {
		t.Fatalf("insecure plan exit = %d: %s", code, stderr.String())
	}
}

// TestHelpIsNotAnError: the parser's own exit is the exit, not a parse
// error after the usage.
func TestHelpIsNotAnError(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	if code := cli.Run(t.Context(), []string{"--help"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("--help exit = %d: %s", code, stderr.String())
	}

	if !strings.Contains(stdout.String(), "Usage:") || stderr.Len() != 0 {
		t.Fatalf("--help wrote:\n%s\n--- stderr:\n%s", stdout.String(), stderr.String())
	}
}

// TestDumpLeavesWhatIsNotADocument: a dotfile does not make a directory
// full, and a note beside the documents is neither removed nor fatal.
func TestDumpLeavesWhatIsNotADocument(t *testing.T) {
	t.Parallel()

	_, flags := house(t)
	dir := t.TempDir()

	if err := xos.WriteFile(filepath.Join(dir, ".gitkeep"), nil, filesystem.FilePermissionsDefault); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	if code := cli.Run(t.Context(), append(flags, "dump", dir), &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("dump into a directory holding a dotfile: exit %d: %s", code, stderr.String())
	}

	note := filepath.Join(dir, "setting", "NOTES.md")
	if err := xos.WriteFile(note, []byte("why\n"), filesystem.FilePermissionsDefault); err != nil {
		t.Fatal(err)
	}

	if code := cli.Run(t.Context(), append(flags, "dump", dir, "--force"), &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("forced dump beside a note: exit %d: %s", code, stderr.String())
	}

	if _, err := xos.Stat(note); err != nil {
		t.Fatalf("the note is gone: %v", err)
	}

	if code := cli.Run(t.Context(), append(flags, "plan", dir), &stdout, &stderr); code != cli.ExitError ||
		!strings.Contains(stderr.String(), "NOTES.md is not a document") {
		t.Fatalf("plan beside a note: exit %d: %s", code, stderr.String())
	}
}
