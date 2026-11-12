// Package cli is unifictl: dump writes the console's documents out as
// files; plan reads both and prints every difference; apply makes them.
// The console's address, site and API key come from the environment:
// UNIFI_URL, UNIFI_SITE and UNIFI_API_KEY; UNIFI_TLS_FINGERPRINT pins its
// self-signed certificate.
package cli

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/mycophonic/primordium/fault"
	"github.com/mycophonic/primordium/filesystem"
	"github.com/mycophonic/primordium/filesystem/flock"

	"github.com/duncanite/go-unifi/internal/console"
	"github.com/duncanite/go-unifi/internal/documents"
	"github.com/duncanite/go-unifi/internal/reconcile"
)

// Exit statuses: plan says whether anything would change the way
// `tofu plan -detailed-exitcode` does.
const (
	ExitOK      = 0
	ExitError   = 1
	ExitChanges = 2
)

// exitStatus is how the parser's own exit, on --help or --version, leaves
// Run: kong calls its exit function and then goes on parsing, so the
// function panics with the status and Run recovers it.
type exitStatus int

// requestTimeout bounds one call to the console; stat/device is the slow
// one, and answers in under a second on the LAN.
const requestTimeout = 30 * time.Second

type rootCommand struct {
	URL         string `default:"https://10.0.0.1"                                                 env:"UNIFI_URL"                                                                        help:"The console: scheme and host."`
	APIKey      string `env:"UNIFI_API_KEY"                                                        help:"API key minted on the console under Control Plane → Integrations."               required:"true"`
	Site        string `default:"default"                                                          env:"UNIFI_SITE"                                                                       help:"The Network site."`
	Fingerprint string `env:"UNIFI_TLS_FINGERPRINT"                                                help:"SHA-256 of the console's certificate, hex, pinned in place of the system roots."`
	Insecure    bool   `help:"Accept any certificate; for the run that reads the fingerprint off."`

	Dump  dumpCommand  `cmd:"" help:"Write every managed document the console holds into the directory."`
	Plan  planCommand  `cmd:"" help:"Show what apply would change. Exit 2 when something would."`
	Apply applyCommand `cmd:"" help:"Make the console match the files."`

	out io.Writer
}

type dumpCommand struct {
	Dir   string `arg:""                                                help:"The documents directory."`
	Force bool   `help:"Write into a directory that already has files."`
}

type planCommand struct {
	Dir string `arg:"" help:"The documents directory." type:"existingdir"`
}

type applyCommand struct {
	Dir string `arg:"" help:"The documents directory." type:"existingdir"`
}

var (
	errChanges  = errors.New("changes pending")
	errNotEmpty = errors.New("directory is not empty; --force overwrites")
)

func (root *rootCommand) reconciler() (*reconcile.Reconciler, error) {
	tlsConfig, err := tlsConfig(root.Fingerprint, root.Insecure)
	if err != nil {
		return nil, err
	}

	// app.Main wraps http.DefaultTransport in primordium's own round
	// tripper, which embeds the *http.Transport and so clones like one; the
	// clone is this client's pool, with the console's TLS in place of the
	// process default, and the wrapper's header and log line left behind.
	cloner, ok := http.DefaultTransport.(interface{ Clone() *http.Transport })
	if !ok {
		return nil, fmt.Errorf("%w: the default transport cannot be cloned", fault.ErrSystemFailure)
	}

	transport := cloner.Clone()
	transport.TLSClientConfig = tlsConfig

	client, err := console.New(
		&http.Client{Transport: transport, Timeout: requestTimeout},
		root.URL,
		root.APIKey,
		root.Site,
	)
	if err != nil {
		return nil, fmt.Errorf("console: %w", err)
	}

	return &reconcile.Reconciler{Console: client}, nil
}

// tlsConfig verifies the console's certificate by its fingerprint when one
// is given, by the system roots otherwise; insecure verifies nothing. The
// console's certificate is self-signed, so the roots alone refuse it.
func tlsConfig(fingerprint string, insecure bool) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12}

	if insecure {
		config.InsecureSkipVerify = true // #nosec G402 -- the flag the user set says so, for the run that reads the fingerprint

		return config, nil
	}

	if fingerprint == "" {
		return config, nil
	}

	pin, err := hex.DecodeString(strings.ToLower(strings.NewReplacer(":", "", " ", "").Replace(fingerprint)))
	if err != nil || len(pin) != sha256.Size {
		return nil, fmt.Errorf("%w: fingerprint %q is not a hex SHA-256", fault.ErrInvalidArgument, fingerprint)
	}

	config.InsecureSkipVerify = true // #nosec G402 -- VerifyConnection below pins the certificate instead
	config.VerifyConnection = func(state tls.ConnectionState) error {
		for _, cert := range state.PeerCertificates {
			sum := sha256.Sum256(cert.Raw)
			if hex.EncodeToString(sum[:]) == hex.EncodeToString(pin) {
				return nil
			}
		}

		return fmt.Errorf(
			"%w: the console's certificate does not match the pinned fingerprint",
			fault.ErrAuthenticationFailure,
		)
	}

	return config, nil
}

// plan loads the files and computes the changes; plan and apply start here.
func (root *rootCommand) plan(ctx context.Context, dir string) (*reconcile.Reconciler, *reconcile.Plan, error) {
	set, err := documents.Load(dir, console.Kinds())
	if err != nil {
		return nil, nil, fmt.Errorf("documents: %w", err)
	}

	rec, err := root.reconciler()
	if err != nil {
		return nil, nil, err
	}

	plan, err := rec.Plan(ctx, set)
	if err != nil {
		return nil, nil, fmt.Errorf("planning: %w", err)
	}

	plan.Write(root.out)

	return rec, plan, nil
}

// locked runs function holding the documents directory's advisory lock:
// exclusive for a command that writes the files or the console, shared for
// plan, so two plans run together and a plan waits for an apply. Two applies
// at once would plan from the same snapshot and both create the same
// documents; in turn, the second finds the first's work and does nothing.
// The lock is this host's: it orders the invocations on one checkout.
func locked(dir string, exclusive bool, function func() error) error {
	defer flock.Cleanup(dir)

	with := flock.WithReadOnlyLock
	if exclusive {
		with = flock.WithLock
	}

	err := with(dir, function)
	if errors.Is(err, flock.ErrLockFail) {
		return fmt.Errorf("locking %s: %w", dir, err)
	}

	return err
}

// Run reads the console before it touches the directory: a path mistyped
// or a console that does not answer leaves nothing behind.
func (cmd *dumpCommand) Run(ctx context.Context, root *rootCommand) error {
	if !cmd.Force {
		empty, err := documents.Empty(cmd.Dir)
		if err != nil {
			return fmt.Errorf("documents: %w", err)
		}

		if !empty {
			return fmt.Errorf("%w: %s", errNotEmpty, cmd.Dir)
		}
	}

	rec, err := root.reconciler()
	if err != nil {
		return err
	}

	set, err := rec.Dump(ctx)
	if err != nil {
		return fmt.Errorf("dumping: %w", err)
	}

	// The directory is the lock, so it exists before the files are written.
	if err := os.MkdirAll(cmd.Dir, filesystem.DirPermissionsDefault); err != nil {
		return fmt.Errorf("%w: creating %s: %w", fault.ErrWriteFailure, cmd.Dir, err)
	}

	return locked(cmd.Dir, true, func() error { return cmd.write(root, set) })
}

func (cmd *dumpCommand) write(root *rootCommand, set *documents.Set) error {
	for _, kind := range console.Kinds() {
		docs, ok := set.Kinds[kind.Name]
		if !ok {
			continue
		}

		orphans, err := documents.Write(cmd.Dir, kind, docs)
		if err != nil {
			return fmt.Errorf("writing %s: %w", kind.Name, err)
		}

		fmt.Fprintf(root.out, "%s: %d documents\n", kind.Name, len(docs))

		// A comment on a key the console no longer has had nowhere to go.
		for _, orphan := range orphans {
			fmt.Fprintf(root.out, "  comment left behind at %s\n", orphan)
		}
	}

	return nil
}

func (cmd *planCommand) Run(ctx context.Context, root *rootCommand) error {
	return locked(cmd.Dir, false, func() error {
		_, plan, err := root.plan(ctx, cmd.Dir)
		if err != nil {
			return err
		}

		if !plan.Empty() {
			return errChanges
		}

		return nil
	})
}

func (cmd *applyCommand) Run(ctx context.Context, root *rootCommand) error {
	return locked(cmd.Dir, true, func() error { return cmd.apply(ctx, root) })
}

func (cmd *applyCommand) apply(ctx context.Context, root *rootCommand) error {
	rec, plan, err := root.plan(ctx, cmd.Dir)
	if err != nil {
		return err
	}

	if plan.Empty() {
		return nil
	}

	if err := rec.Apply(ctx, plan); err != nil {
		return fmt.Errorf("applying: %w", err)
	}

	fmt.Fprintln(root.out, "Applied.")

	return nil
}

// Run parses args and runs the command, writing the plan to stdout and
// usage to stderr, and returns the exit status. The context is bound to
// the interface type the Run methods take; kong binds by concrete type
// otherwise, and the one app.Main hands out is not context.Context.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) (status int) {
	defer func() {
		if recovered := recover(); recovered != nil {
			exited, ok := recovered.(exitStatus)
			if !ok {
				panic(recovered)
			}

			status = int(exited)
		}
	}()

	root := rootCommand{out: stdout}

	parser, err := kong.New(&root,
		kong.Name("unifictl"),
		kong.Description("Make the console match the files."),
		kong.UsageOnError(),
		kong.Writers(stdout, stderr),
		kong.Exit(func(code int) { panic(exitStatus(code)) }),
		kong.BindTo(ctx, (*context.Context)(nil)),
	)
	if err != nil {
		fmt.Fprintln(stderr, "unifictl:", err)

		return ExitError
	}

	kctx, err := parser.Parse(args)
	if err != nil {
		parser.Errorf("%s", err)

		return ExitError
	}

	err = kctx.Run(&root)

	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, errChanges):
		return ExitChanges
	default:
		fmt.Fprintln(stderr, "unifictl:", err)

		return ExitError
	}
}
