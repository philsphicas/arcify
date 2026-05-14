// Package runner orchestrates the in-VM script via the action-style
// runCommand API (POST .../virtualMachines/{vm}/runCommand) and polls the
// Arc machine resource for connected status.
//
// The action-style API differs from the resource-style API
// (Microsoft.Compute/virtualMachines/runCommands) in three ways arcify cares
// about:
//
//  1. No tracked ARM resource is created. Nothing to clean up afterwards.
//  2. No `protectedParameters` field. Parameters travel in the request body
//     just like any other ARM call.
//  3. The script's exit code is NOT returned. The response always reports
//     ProvisioningState/succeeded as long as the agent ran the script.
//
// Because of (3), we cannot use the script's exit code to detect failure.
// Instead, the in-VM scripts print `ARCIFY_RESULT=success` as the very last
// line of their successful path. The runner parses combined stdout/stderr
// from the response and flags Succeeded = true iff the sentinel is present.
package runner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/hybridcompute/armhybridcompute"

	"github.com/philsphicas/arcify/internal/arm"
)

// successSentinel is the marker the in-VM scripts print on a successful
// run. The exact text is intentionally short and unique enough not to
// collide with anything azcmagent or the install script print.
const successSentinel = "ARCIFY_RESULT=success"

// Input describes a single runCommand dispatch.
type Input struct {
	ResourceGroup string
	VMName        string
	OSType        string // "Linux" or "Windows" (used to pick commandId)
	ScriptBody    string
	ParameterName string
	ParameterB64  string
}

// Result is a parsed view of the action-style runCommand response, used for
// stderr progress logging and success classification.
type Result struct {
	// Succeeded is true iff the in-VM script printed `ARCIFY_RESULT=success`
	// as the final non-empty line of its stdout. arcify uses this as a proxy
	// for "script's view of success" because the action-style runCommand API
	// does not return the script's exit code. (If the markers can't be
	// parsed at all, we fall back to a substring search on the whole message
	// — see parseResult.)
	Succeeded bool

	// Stdout and Stderr are extracted from the response when possible. The
	// action-style API typically returns a single InstanceViewStatus whose
	// Message field contains both streams inlined with `[stdout]` and
	// `[stderr]` markers; we split on those. If the markers aren't present
	// the whole Message ends up in Stdout.
	Stdout string
	Stderr string

	// Combined is the raw Message field(s) joined, kept verbatim for cases
	// where the marker-split parsing isn't useful.
	Combined string
}

// Dispatch issues the action-style runCommand POST. When wait is true, it
// blocks until the in-VM script terminates (or the context expires); when
// false, it submits the call and returns as soon as ARM accepts the POST.
//
// Returns:
//   - the raw SDK result. nil if the LRO didn't produce one — e.g.
//     --no-wait, or the poll context expired before completion.
//   - a parsed *Result summarizing stdout/stderr/sentinel-success. nil if
//     no result was retrieved.
//   - accepted: true iff ARM accepted the POST. Callers should track this
//     independently from the result so the dispatch isn't forgotten when a
//     poll timeout leaves the result nil.
//   - an error iff the POST failed or the LRO poll hit a non-recoverable
//     error. A script that returns non-zero is NOT an error here (we can't
//     detect that anyway); inspect Result.Succeeded.
func Dispatch(
	ctx context.Context,
	vmClient *armcompute.VirtualMachinesClient,
	in Input,
	wait bool,
) (*armcompute.RunCommandResult, *Result, bool, error) {
	commandID := commandIDFor(in.OSType)
	body := armcompute.RunCommandInput{
		CommandID: &commandID,
		Script:    []*string{&in.ScriptBody},
		Parameters: []*armcompute.RunCommandInputParameter{
			{Name: &in.ParameterName, Value: &in.ParameterB64},
		},
	}

	poller, err := vmClient.BeginRunCommand(ctx, in.ResourceGroup, in.VMName, body, nil)
	if err != nil {
		// POST didn't even register — nothing to report.
		return nil, nil, false, fmt.Errorf("BeginRunCommand: %w", err)
	}

	if !wait {
		// Fire-and-forget: don't poll. The agent still runs the script
		// server-side, but the action API gives us no way to retrieve the
		// result later (no tracked resource).
		return nil, nil, true, nil
	}

	resp, perr := poller.PollUntilDone(ctx, nil)
	if perr != nil {
		// Poll error — we have no result to surface. The script may or may
		// not have completed server-side.
		return nil, nil, true, fmt.Errorf("runCommand poll: %w", perr)
	}
	result := parseResult(&resp.RunCommandResult)
	return &resp.RunCommandResult, result, true, nil
}

func commandIDFor(osType string) string {
	if strings.EqualFold(osType, "windows") {
		return "RunPowerShellScript"
	}
	return "RunShellScript"
}

// parseResult extracts stdout/stderr and the success sentinel from an
// action-style runCommand response. The agent typically returns a single
// InstanceViewStatus whose Message field is shaped like:
//
// Enable succeeded:
// [stdout]
// <stdout content>
//
// [stderr]
// <stderr content>
//
// We split on the marker lines. If the markers aren't present, the entire
// Message ends up in Stdout for diagnosis.
func parseResult(r *armcompute.RunCommandResult) *Result {
	if r == nil {
		return nil
	}
	var combinedMessages []string
	for _, iv := range r.Value {
		if iv != nil && iv.Message != nil {
			combinedMessages = append(combinedMessages, *iv.Message)
		}
	}
	combined := strings.Join(combinedMessages, "\n")
	stdout, stderr := splitStdoutStderr(combined)

	// Primary check: ARCIFY_RESULT=success must be the final non-empty line
	// of stdout. This matches the contract the in-VM scripts implement.
	// Fallback: if marker parsing produced no stdout/stderr split at all
	// (combined is unchanged), allow a substring match in the raw message
	// for robustness against future agent format changes.
	succeeded := isLastNonEmptyLine(stdout, successSentinel)
	if !succeeded && stdout == "" && stderr == "" && strings.Contains(combined, successSentinel) {
		succeeded = true
	}

	return &Result{
		Succeeded: succeeded,
		Stdout:    stdout,
		Stderr:    stderr,
		Combined:  combined,
	}
}

// splitStdoutStderr parses the agent's "[stdout]...[stderr]..." message
// format. The markers always appear on their own line in the responses we've
// observed; we anchor on `\n[stdout]\n` and `\n[stderr]\n` to avoid matching
// inside script output.
func splitStdoutStderr(msg string) (stdout, stderr string) {
	normalized := "\n" + msg
	const (
		outMarker = "\n[stdout]\n"
		errMarker = "\n[stderr]\n"
	)
	outIdx := strings.Index(normalized, outMarker)
	errIdx := strings.Index(normalized, errMarker)
	switch {
	case outIdx < 0 && errIdx < 0:
		// No markers — treat the whole thing as stdout.
		return msg, ""
	case outIdx >= 0 && errIdx >= 0 && errIdx > outIdx:
		stdout = strings.TrimRight(normalized[outIdx+len(outMarker):errIdx], "\n")
		stderr = strings.TrimRight(normalized[errIdx+len(errMarker):], "\n")
	case outIdx >= 0:
		stdout = strings.TrimRight(normalized[outIdx+len(outMarker):], "\n")
	case errIdx >= 0:
		stderr = strings.TrimRight(normalized[errIdx+len(errMarker):], "\n")
	}
	return stdout, stderr
}

func isLastNonEmptyLine(s, want string) bool {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\n' || s[i] == ' ' || s[i] == '\t' || s[i] == '\r' {
			continue
		}
		// Walk back to the start of this line.
		end := i + 1
		start := strings.LastIndexByte(s[:end], '\n') + 1
		return strings.TrimRight(s[start:end], " \t\r") == want
	}
	return false
}

// VerifyConnected polls the Arc machine resource until properties.status ==
// "Connected" or the context deadline elapses. Returns the parsed status and
// the raw SDK response (non-nil when at least one Get succeeded) so the
// caller has a fresh snapshot to emit even on failure.
func VerifyConnected(
	ctx context.Context,
	arcClient *arm.ArcMachineClient,
	rg, name string,
) (*arm.ArcStatus, *armhybridcompute.MachinesClientGetResponse, error) {
	backoff := 3 * time.Second
	const maxBackoff = 15 * time.Second
	var lastResp *armhybridcompute.MachinesClientGetResponse
	for {
		resp, err := arcClient.Get(ctx, rg, name)
		if err == nil {
			lastResp = &resp
			status := parseStatus(&resp)
			if status.Status == "Connected" {
				return status, lastResp, nil
			}
		}
		select {
		case <-ctx.Done():
			lastStatus := "<empty>"
			if lastResp != nil {
				lastStatus = parseStatus(lastResp).Status
				if lastStatus == "" {
					lastStatus = "<empty>"
				}
			}
			if err != nil && lastResp == nil {
				return nil, nil, fmt.Errorf("last Get error: %w; ctx: %w", err, ctx.Err())
			}
			return parseStatus(lastResp), lastResp, fmt.Errorf("timed out waiting for Connected; last status=%s", lastStatus)
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

func parseStatus(resp *armhybridcompute.MachinesClientGetResponse) *arm.ArcStatus {
	out := &arm.ArcStatus{}
	if resp == nil || resp.Properties == nil {
		return out
	}
	if resp.Properties.Status != nil {
		out.Status = string(*resp.Properties.Status)
	}
	if resp.Properties.AgentVersion != nil {
		out.AgentVersion = *resp.Properties.AgentVersion
	}
	return out
}
