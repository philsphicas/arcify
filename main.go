// arcify Arc-enrolls an existing Azure VM end-to-end:
//   - precreates the Microsoft.HybridCompute/machines resource with a freshly
//     generated public key,
//   - dispatches the embedded enrollment script via the action-style
//     runCommand API with the matching private key as a parameter,
//   - optionally waits for the agent to register and the resource to reach
//     status=Connected.
//
// Output model is plain text, in the style of small Unix utilities:
//   - stdout — the Arc machine ARM ID on success, nothing otherwise.
//   - stderr — human-readable progress and any final error message; on
//     failure, the captured in-VM script output is also dumped here for
//     diagnosis.
//
// Unlike the resource-style runCommand API, the action-style API does not
// create a tracked ARM resource. There is nothing to clean up after the
// script runs, which keeps the tail latency on a successful enrollment to
// just the time it takes to verify Arc-Connected status.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	"github.com/philsphicas/arcify/internal/arm"
	"github.com/philsphicas/arcify/internal/armid"
	"github.com/philsphicas/arcify/internal/keypair"
	"github.com/philsphicas/arcify/internal/payload"
	"github.com/philsphicas/arcify/internal/runner"
	"github.com/philsphicas/arcify/internal/scripts"
)

var version = "dev"

// Exit codes — kept to the smallest justifiable set:
//
//	0 — success.
//	1 — any operational failure. stderr describes what went wrong, and on
//	    a partial failure points the user at any ARM resource arcify left
//	    behind.
//	2 — bad CLI args. Matches what the Go `flag` package returns from
//	    fs.Parse on a malformed flag, and the long-standing POSIX/getopt
//	    convention for misuse.
//
// We intentionally do NOT use 130 (SIGINT-conventional) on ^C: the exit code
// only needs to communicate success / failure / bad-args.
const (
	exitOK      = 0
	exitFailure = 1
	exitBadArgs = 2
)

type options struct {
	vmARMID     string
	arcSub      string
	arcRG       string
	arcName     string
	arcLocation string
	arcTenant   string
	tags        string
	wait        time.Duration
	noWait      bool
	force       bool
	dryRun      bool
	verbose     bool
	showVersion bool
	precreate   bool
	output      string
}

func main() {
	os.Exit(realMain(os.Args[1:], os.Stdout, os.Stderr))
}

// realMain is the testable entrypoint: it accepts the args slice, the writers
// to use for stdout (Arc machine ARM ID or precreate payload on success) and
// stderr (progress and errors), and returns an exit code.
func realMain(args []string, stdout, stderr io.Writer) int {
	opts, err := parseFlags(args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "arcify:", err)
		return exitBadArgs
	}
	if opts.showVersion {
		fmt.Fprintln(stderr, "arcify", version)
		return exitOK
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log := &logger{w: stderr, verbose: opts.verbose}

	if opts.precreate {
		if err := runPrecreate(ctx, opts, log, stdout); err != nil {
			fmt.Fprintln(stderr, "arcify:", log.redact(err.Error()))
			return exitFailure
		}
		return exitOK
	}

	arcID, runErr := run(ctx, opts, log)

	if runErr != nil {
		fmt.Fprintln(stderr, "arcify:", log.redact(runErr.Error()))
		return exitFailure
	}
	if arcID != "" {
		fmt.Fprintln(stdout, arcID)
	}
	return exitOK
}

// run executes the full Arc-enrollment flow. On full success it returns the
// ARM ID of the Arc machine resource; on partial failure (e.g. Arc resource
// created but agent never reached Connected) it returns "" plus an error,
// having already logged the leftover ARM ID to stderr so the user can clean
// up or retry with --force.
func run(ctx context.Context, opts *options, log *logger) (string, error) {
	// 1. Parse VM ARM ID.
	vmID, err := armid.ParseVM(opts.vmARMID)
	if err != nil {
		return "", fmt.Errorf("parse VM ARM ID: %w", err)
	}

	arcSub := firstNonEmpty(opts.arcSub, vmID.SubscriptionID)
	arcRG := firstNonEmpty(opts.arcRG, vmID.ResourceGroup)
	arcName := firstNonEmpty(opts.arcName, vmID.Name)
	tags, err := parseTags(opts.tags)
	if err != nil {
		return "", fmt.Errorf("parse --tags: %w", err)
	}
	arcID := arcMachineARMID(arcSub, arcRG, arcName)

	// 2. Auth. Allow cross-tenant token acquisition so a single
	// DefaultAzureCredential can hit both the VM's subscription and the Arc
	// subscription when they live in different tenants.
	cred, err := azidentity.NewDefaultAzureCredential(&azidentity.DefaultAzureCredentialOptions{
		AdditionallyAllowedTenants: []string{"*"},
	})
	if err != nil {
		return "", fmt.Errorf("acquire Azure credentials: %w", err)
	}

	clients, err := arm.NewClients(vmID.SubscriptionID, arcSub, cred)
	if err != nil {
		return "", fmt.Errorf("build ARM clients: %w", err)
	}

	// 3. Discover VM.
	vm, err := clients.VM.Get(ctx, vmID.ResourceGroup, vmID.Name)
	if err != nil {
		return "", fmt.Errorf("lookup VM %s: %w", vmID.ARMID, err)
	}
	if !vm.ProvisionVMAgent {
		return "", fmt.Errorf("VM %s does not have the Azure Guest Agent enabled (provisionVMAgent / AllowExtensionOperations is false); runCommand cannot be used", vmID.Name)
	}
	arcLocation := firstNonEmpty(opts.arcLocation, vm.Location)
	log.Infof("discovered VM %s (%s, %s)", vmID.Name, vm.OSType, vm.Location)

	// 4. Tenant lookup against the Arc subscription — this is the tenant
	// azcmagent registers into, which may differ from the VM's tenant.
	tenantID, err := clients.Tenant.Get(ctx, arcSub)
	if err != nil {
		return "", fmt.Errorf("lookup tenant for Arc subscription %s: %w", arcSub, err)
	}

	// 5. Keypair (in memory only — never written to disk).
	kp, err := keypair.Generate()
	if err != nil {
		return "", fmt.Errorf("generate keypair: %w", err)
	}
	log.Infof("generated RSA-2048 keypair (vmId %s)", shortID(kp.VMID))
	// Register the inner private key for redaction. The wire-format
	// credential blob is added after BuildConfigJSON below. These guard
	// against any future code path (an azcmagent error message, a script
	// that accidentally echoes its argv, etc.) that might surface the
	// secret in stderr.
	log.redactSecret(kp.PrivateKeyDERBase64)

	if opts.dryRun {
		log.Infof("--dry-run set; skipping all ARM mutations")
		log.Infof("would create Arc machine: %s", arcID)
		log.Infof("would dispatch %s script via runCommand to %s/%s/%s",
			strings.ToLower(vm.OSType), vmID.SubscriptionID, vmID.ResourceGroup, vmID.Name)
		return "", nil
	}

	// 6. Create Arc machine resource.
	if err := clients.ArcMachine.CreateOrUpdate(ctx, arm.CreateArcMachineInput{
		ResourceGroup: arcRG,
		Name:          arcName,
		Location:      arcLocation,
		Tags:          tags,
		PublicKeyB64:  kp.PublicKeyDERBase64,
		VMID:          kp.VMID,
		Force:         opts.force,
	}); err != nil {
		if errors.Is(err, arm.ErrArcResourceConflict) {
			return "", fmt.Errorf("arc machine %s already exists; pass --force to overwrite", arcID)
		}
		return "", fmt.Errorf("create Arc machine resource: %w", err)
	}
	log.Infof("created Arc machine %s and uploaded public key", arcID)
	// From here on, any failure leaves the Arc resource in ARM. Defer a
	// one-shot reminder so callers always know where to look or what to
	// pass to `az resource delete` / `arcify --force`.
	arcLeftBehind := true
	defer func() {
		if arcLeftBehind {
			log.Infof("left Arc machine %s behind; rerun with --force to retry or 'az resource delete --ids %s' to clean up", arcID, arcID)
		}
	}()

	// 7. Build credential JSON (base64-encoded).
	credentialB64, err := scripts.BuildConfigJSON(scripts.Config{
		SubscriptionID: arcSub,
		ResourceGroup:  arcRG,
		Location:       arcLocation,
		TenantID:       tenantID,
		ResourceName:   arcName,
		VMID:           kp.VMID,
		PrivateKey:     kp.PrivateKeyDERBase64,
	})
	if err != nil {
		return "", fmt.Errorf("build credentials: %w", err)
	}
	log.redactSecret(credentialB64)

	// 8. Pick the right embedded script.
	scriptBody, paramName := scripts.For(vm.OSType)

	rcInput := runner.Input{
		ResourceGroup: vmID.ResourceGroup,
		VMName:        vmID.Name,
		OSType:        vm.OSType,
		ScriptBody:    scriptBody,
		ParameterName: paramName,
		ParameterB64:  credentialB64,
	}

	// 9. Dispatch runCommand. In --no-wait mode we don't poll the LRO; in
	// --wait mode we use a client-side deadline so we don't block forever.
	// The action-style runCommand API has no client-controllable server
	// timeout (the agent enforces ~90 min server-side); --wait is the
	// client's observation budget.
	dispatchCtx := ctx
	var dispatchCancel context.CancelFunc
	if !opts.noWait {
		dispatchCtx, dispatchCancel = context.WithTimeout(ctx, opts.wait)
		defer dispatchCancel()
	}
	if opts.noWait {
		log.Infof("dispatching runCommand to %s (%s script, --no-wait)", vmID.Name, strings.ToLower(vm.OSType))
	} else {
		log.Infof("dispatching runCommand to %s (%s script, wait up to %s)", vmID.Name, strings.ToLower(vm.OSType), opts.wait)
	}
	_, rcResult, _, err := runner.Dispatch(dispatchCtx, clients.VMRaw, rcInput, !opts.noWait)

	if opts.noWait {
		// Fire-and-forget. An error from Dispatch in this mode means the
		// POST to ARM was rejected — the agent never even saw the script.
		// Propagate it as a real failure.
		if err != nil {
			return "", fmt.Errorf("runCommand: %w", err)
		}
		log.Infof("not waiting for completion (--no-wait); script output is unrecoverable")
		arcLeftBehind = false
		return arcID, nil
	}

	if err != nil {
		if rcResult != nil {
			logScriptOutput(log, rcResult)
		}
		return "", fmt.Errorf("runCommand: %w", err)
	}
	if rcResult == nil {
		return "", errors.New("runCommand returned no result")
	}
	if !rcResult.Succeeded {
		// The action API does not surface the script's exit code, so we
		// rely on the script's ARCIFY_RESULT=success sentinel. If absent,
		// something failed in-VM — surface its output and bail without
		// waiting for Arc-Connected (which would never happen).
		logScriptOutput(log, rcResult)
		return "", errors.New("in-VM script did not report success (sentinel ARCIFY_RESULT=success missing)")
	}
	log.Infof("runCommand completed (success sentinel present)")

	// 10. Verify Connected, sharing the same client-side deadline.
	log.Infof("polling Arc machine status (within remaining wait budget)")
	status, _, verr := runner.VerifyConnected(dispatchCtx, clients.ArcMachine, arcRG, arcName)
	if verr != nil {
		return "", fmt.Errorf("verify Connected: %w", verr)
	}
	log.Infof("Arc machine status: %s, agent %s", status.Status, status.AgentVersion)

	arcLeftBehind = false
	return arcID, nil
}

// arcMachineARMID composes the canonical ARM ID for a Microsoft.HybridCompute
// machine. We construct it from the components we already have rather than
// extracting it from a GET response so the value is available even before
// the resource exists in ARM.
func arcMachineARMID(sub, rg, name string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.HybridCompute/machines/%s", sub, rg, name)
}

func logScriptOutput(log *logger, r *runner.Result) {
	if r.Stderr != "" {
		log.Infof("---script stderr---\n%s", log.redact(r.Stderr))
	}
	if r.Stdout != "" {
		log.Infof("---script stdout---\n%s", log.redact(r.Stdout))
	}
	if r.Stdout == "" && r.Stderr == "" && r.Combined != "" {
		log.Infof("---script output (unparsed)---\n%s", log.redact(r.Combined))
	}
}

// runPrecreate is the precreate-only flow: it generates a fresh RSA keypair,
// creates the Microsoft.HybridCompute/machines resource with the public key,
// resolves the Arc subscription's tenant, and writes the connection payload
// (private key + identity fields) to stdout in the chosen format.
//
// The consumer (e.g. an azcmagent-running container) is expected to complete
// the second half — `azcmagent connect existing --private-key ...` — itself.
//
// Failure semantics mirror the end-to-end run(): if the Arc resource is
// successfully created but a subsequent step (tenant resolution, payload
// emission) fails, the resource is left in ARM and the user gets a stderr
// reminder pointing at its ARM ID so they can retry with --force or clean
// up.
//
// stdout is written ONLY after the payload formatter returns success; that's
// what closes the window in which an emission failure could lose the only
// copy of the private key.
func runPrecreate(ctx context.Context, opts *options, log *logger, stdout io.Writer) error {
	arcSub := opts.arcSub
	arcRG := opts.arcRG
	arcName := opts.arcName
	arcLocation := opts.arcLocation
	tags, err := parseTags(opts.tags)
	if err != nil {
		return fmt.Errorf("parse --tags: %w", err)
	}
	arcID := arcMachineARMID(arcSub, arcRG, arcName)

	cred, err := azidentity.NewDefaultAzureCredential(&azidentity.DefaultAzureCredentialOptions{
		AdditionallyAllowedTenants: []string{"*"},
	})
	if err != nil {
		return fmt.Errorf("acquire Azure credentials: %w", err)
	}

	// We have no VM in this mode, so reuse arcSub for both client
	// subscription IDs. The VM client is built but never used.
	clients, err := arm.NewClients(arcSub, arcSub, cred)
	if err != nil {
		return fmt.Errorf("build ARM clients: %w", err)
	}

	if opts.dryRun {
		log.Infof("--dry-run set; skipping all ARM mutations")
		log.Infof("would create Arc machine: %s", arcID)
		log.Infof("would resolve tenant for subscription %s (or use --arc-tenant override %q)", arcSub, opts.arcTenant)
		log.Infof("would generate RSA-2048 keypair and emit payload as --output=%s", opts.output)
		return nil
	}

	var tenantID string
	if opts.arcTenant != "" {
		tenantID = opts.arcTenant
		log.Infof("using --arc-tenant override: %s", tenantID)
	} else {
		tenantID, err = clients.Tenant.Get(ctx, arcSub)
		if err != nil {
			return fmt.Errorf("lookup tenant for Arc subscription %s (set --arc-tenant to override): %w", arcSub, err)
		}
		log.Infof("resolved tenant for Arc subscription: %s", tenantID)
	}

	kp, err := keypair.Generate()
	if err != nil {
		return fmt.Errorf("generate keypair: %w", err)
	}
	log.Infof("generated RSA-2048 keypair (vmId %s)", shortID(kp.VMID))
	log.redactSecret(kp.PrivateKeyDERBase64)

	if err := clients.ArcMachine.CreateOrUpdate(ctx, arm.CreateArcMachineInput{
		ResourceGroup: arcRG,
		Name:          arcName,
		Location:      arcLocation,
		Tags:          tags,
		PublicKeyB64:  kp.PublicKeyDERBase64,
		VMID:          kp.VMID,
		Force:         opts.force,
	}); err != nil {
		if errors.Is(err, arm.ErrArcResourceConflict) {
			return fmt.Errorf("arc machine %s already exists; pass --force to overwrite", arcID)
		}
		return fmt.Errorf("create Arc machine resource: %w", err)
	}
	log.Infof("created Arc machine %s and uploaded public key", arcID)

	arcLeftBehind := true
	defer func() {
		if arcLeftBehind {
			log.Infof("left Arc machine %s behind; rerun with --force to retry or 'az resource delete --ids %s' to clean up", arcID, arcID)
		}
	}()

	p := payload.Payload{
		ArcResourceID:  arcID,
		SubscriptionID: arcSub,
		ResourceGroup:  arcRG,
		Name:           arcName,
		Location:       arcLocation,
		TenantID:       tenantID,
		VMID:           kp.VMID,
		PrivateKey:     kp.PrivateKeyDERBase64,
	}

	var emitErr error
	switch opts.output {
	case "json":
		emitErr = payload.FormatJSON(stdout, p)
	default:
		emitErr = payload.FormatEnv(stdout, p)
	}
	if emitErr != nil {
		return fmt.Errorf("emit payload: %w", emitErr)
	}

	arcLeftBehind = false
	log.Infof("precreate complete; payload written to stdout. Feed it to your consumer (e.g. 'docker run --env-file ...').")
	return nil
}

// ---- flag parsing ----

func parseFlags(args []string, errOut io.Writer) (*options, error) {
	fs := flag.NewFlagSet("arcify", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `arcify — Arc-enroll an Azure VM in one command.

Usage:
  arcify <vm-arm-id> [flags]                    end-to-end enrollment
  arcify --precreate --arc-subscription ... \
         --arc-rg ... --arc-name ... \
         --arc-location ... [flags]             precreate-only, emit payload

Modes:
  Default: take an existing Azure VM, generate a keypair, pre-create the
  Arc resource, dispatch the in-VM enrollment script via runCommand, and
  verify Connected. On success, the Arc machine ARM ID is printed to stdout.

  --precreate: create the Arc resource and emit a payload (private key,
  tenant id, identity fields) to stdout — useful when the second half of
  the connection is done by a different consumer (e.g. a container running
  'azcmagent connect existing'). No VM is contacted; only --arc-* flags are
  used. Output format defaults to Docker env-file; --output json switches to
  JSON. Recommended: 'umask 077' before redirecting stdout to a file.

Output:
  stdout — Arc machine ARM ID on success in normal mode; the connection
           payload in --precreate mode. Empty on failure or --dry-run.
  stderr — human-readable progress and any final error message. On normal-
           mode failure, the captured in-VM script output is also dumped
           here for diagnosis.

Flags:
`)
		fs.PrintDefaults()
	}

	opts := &options{}
	fs.StringVar(&opts.arcSub, "arc-subscription", "", "Arc machine subscription ID (default: VM's subscription; required with --precreate)")
	fs.StringVar(&opts.arcRG, "arc-rg", "", "Arc machine resource group (default: VM's RG; required with --precreate)")
	fs.StringVar(&opts.arcName, "arc-name", "", "Arc machine name (default: VM's name; required with --precreate)")
	fs.StringVar(&opts.arcLocation, "arc-location", "", "Arc machine location (default: VM's location; required with --precreate)")
	fs.StringVar(&opts.arcTenant, "arc-tenant", "", "Tenant ID override (default: looked up from the Arc subscription)")
	fs.StringVar(&opts.tags, "tags", "", "Comma-separated tags (k=v,k=v)")
	fs.DurationVar(&opts.wait, "wait", 5*time.Minute, "Clock-time budget to wait for runCommand + Arc-Connected verification")
	fs.BoolVar(&opts.noWait, "no-wait", false, "Issue both ARM resources and exit immediately, without waiting for the script or verifying status")
	fs.BoolVar(&opts.force, "force", false, "Recreate Arc resource if a conflict exists")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "Print the plan and exit; touch nothing in ARM")
	fs.BoolVar(&opts.verbose, "verbose", false, "More logging")
	fs.BoolVar(&opts.verbose, "v", false, "More logging (shorthand)")
	fs.BoolVar(&opts.showVersion, "version", false, "Print version and exit")
	fs.BoolVar(&opts.precreate, "precreate", false, "Precreate-only mode: create the Arc resource and emit a payload (no VM contacted)")
	fs.StringVar(&opts.output, "output", "env", "Payload output format for --precreate: 'env' (Docker --env-file) or 'json'")

	args = reorderArgs(args, fs)

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if opts.showVersion {
		return opts, nil
	}

	if opts.precreate {
		if fs.NArg() > 0 {
			return nil, fmt.Errorf("--precreate does not take a positional <vm-arm-id> argument; got %v", fs.Args())
		}
		if opts.arcSub == "" || opts.arcRG == "" || opts.arcName == "" || opts.arcLocation == "" {
			return nil, errors.New("--precreate requires --arc-subscription, --arc-rg, --arc-name, --arc-location")
		}
		if opts.noWait {
			return nil, errors.New("--no-wait is not valid with --precreate (no runCommand is dispatched)")
		}
		switch opts.output {
		case "env", "json":
		default:
			return nil, fmt.Errorf("--output must be 'env' or 'json', got %q", opts.output)
		}
		return opts, nil
	}

	// Non-precreate validation.
	if fs.NArg() < 1 {
		fs.Usage()
		return nil, errors.New("missing required <vm-arm-id> argument")
	}
	if fs.NArg() > 1 {
		return nil, fmt.Errorf("unexpected extra arguments: %v", fs.Args()[1:])
	}
	opts.vmARMID = fs.Arg(0)
	if opts.wait < 0 {
		return nil, fmt.Errorf("--wait must be non-negative, got %s", opts.wait)
	}
	if isFlagSet(fs, "output") {
		return nil, errors.New("--output is only valid with --precreate")
	}
	if opts.arcTenant != "" {
		return nil, errors.New("--arc-tenant is only valid with --precreate (in normal mode the tenant is always resolved from the Arc subscription)")
	}
	return opts, nil
}

// isFlagSet returns true iff the user supplied the flag (vs. it sitting at
// its default value). flag.FlagSet.Visit iterates only those that were
// actually set on the command line.
func isFlagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// reorderArgs lets users put flags after the positional <vm-arm-id>, which
// the stdlib flag parser otherwise wouldn't accept. We move any token
// looking like a flag (and its value, when the flag takes one) ahead of the
// positional argument.
func reorderArgs(args []string, fs *flag.FlagSet) []string {
	flags := make([]string, 0, len(args))
	positional := make([]string, 0, 2)
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			flags = append(flags, args[i:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			i++
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			i++
			continue
		}
		if takesValue(fs, name) && i+1 < len(args) {
			flags = append(flags, args[i+1])
			i += 2
			continue
		}
		i++
	}
	return append(flags, positional...)
}

func takesValue(fs *flag.FlagSet, name string) bool {
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
		return false
	}
	return true
}

// ---- helpers ----

// logger writes "arcify: ..." lines to its writer. It redacts any secret
// values registered via redactSecret before writing. The redactor also
// exposes redact() for callers that need to scrub strings they assemble
// themselves (e.g. the final error message printed in realMain).
type logger struct {
	w       io.Writer
	verbose bool
	secrets []string
}

func (l *logger) redactSecret(s string) {
	if s == "" {
		return
	}
	l.secrets = append(l.secrets, s)
}

func (l *logger) redact(s string) string {
	for _, sec := range l.secrets {
		s = strings.ReplaceAll(s, sec, "<REDACTED>")
	}
	return s
}

func (l *logger) Infof(format string, args ...any) {
	msg := l.redact(fmt.Sprintf(format, args...))
	fmt.Fprintln(l.w, "arcify:", msg)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func shortID(id string) string {
	if len(id) >= 8 {
		return id[:8] + "…"
	}
	return id
}

func parseTags(s string) (map[string]string, error) {
	if s == "" {
		return nil, nil
	}
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid tag %q (expected k=v)", kv)
		}
		out[k] = v
	}
	return out, nil
}
