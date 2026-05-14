// Package payload renders the "precreate" mode's connection bundle —
// everything the consumer (e.g. an azcmagent-running container) needs to
// invoke `azcmagent connect existing` against a pre-created Arc machine
// resource.
//
// Two output formats are supported:
//
//   - FormatEnv: Docker --env-file compatible (KEY=VALUE per line, no
//     quoting). This format works directly with `docker run --env-file`.
//     It is NOT promised to be a drop-in for `set -a; source` — that needs
//     shell escaping which Docker's parser intentionally doesn't honour.
//   - FormatJSON: a JSON object, useful for piping into tools that want
//     structured input.
package payload

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// Payload is the connection bundle. Field names map to both env-var keys
// and JSON keys via the explicit conventions implemented in the formatters
// (rather than by reflection), so the wire format stays decoupled from
// internal struct layout.
type Payload struct {
	ArcResourceID  string
	SubscriptionID string
	ResourceGroup  string
	Name           string
	Location       string
	TenantID       string
	VMID           string
	// PrivateKey is base64-encoded PKCS#1 DER. The consumer passes it to
	// `azcmagent connect existing --private-key`.
	PrivateKey string //nolint:gosec // G117: this struct intentionally carries the private key — it is the precreate-mode output schema; emitting it is the whole point of the mode.
}

// FormatEnv writes a Docker `--env-file` compatible representation to w.
// The first non-empty line is a comment with provenance info; the
// remaining lines are bare KEY=VALUE pairs in a deterministic order so the
// output is diff-friendly.
//
// No quoting or escaping is applied. All values arcify generates here
// (UUIDs, base64, locations, Azure resource identifiers) are safe under
// Docker's parser — single lines, no embedded `=` or newlines. If a future
// caller threads in user-supplied values that may contain those characters,
// we'd need to reject them upstream.
func FormatEnv(w io.Writer, p Payload) error {
	header := fmt.Sprintf("# arcify --precreate payload (generated %s UTC)\n# Pass to `docker run --env-file <file>` or treat as opaque key/value pairs.\n# Format is Docker env-file; do NOT `source` directly (no shell escaping).\n\n",
		time.Now().UTC().Format(time.RFC3339))

	if _, err := io.WriteString(w, header); err != nil {
		return err
	}

	lines := []struct {
		key, value string
	}{
		{"ARC_RESOURCE_ID", p.ArcResourceID},
		{"ARC_SUBSCRIPTION_ID", p.SubscriptionID},
		{"ARC_RESOURCE_GROUP", p.ResourceGroup},
		{"ARC_RESOURCE_NAME", p.Name},
		{"ARC_LOCATION", p.Location},
		{"ARC_TENANT_ID", p.TenantID},
		{"ARC_VMID", p.VMID},
		{"ARC_PRIVATE_KEY", p.PrivateKey},
	}
	for _, kv := range lines {
		if strings.ContainsAny(kv.value, "\r\n") {
			return fmt.Errorf("payload value for %s contains a newline — refusing to emit a corrupt env file", kv.key)
		}
		if _, err := fmt.Fprintf(w, "%s=%s\n", kv.key, kv.value); err != nil {
			return err
		}
	}
	return nil
}

// FormatJSON writes the payload as a single JSON object (pretty-printed with
// two-space indents) terminated by a newline.
func FormatJSON(w io.Writer, p Payload) error {
	obj := map[string]string{
		"arcResourceId":  p.ArcResourceID,
		"subscriptionId": p.SubscriptionID,
		"resourceGroup":  p.ResourceGroup,
		"name":           p.Name,
		"location":       p.Location,
		"tenantId":       p.TenantID,
		"vmId":           p.VMID,
		"privateKey":     p.PrivateKey,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(obj)
}
