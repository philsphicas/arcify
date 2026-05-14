// Package scripts contains the in-VM enrollment scripts (embedded via
// //go:embed) and helpers for building the credential JSON that gets handed
// to them at runCommand dispatch time.
package scripts

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed arc-enroll.sh
var linuxScript string

//go:embed arc-enroll.ps1
var windowsScript string

// Config is the in-VM script's input schema. Field names match the JSON keys
// the script expects. The whole struct is base64-encoded JSON and travels in
// the action-style runCommand API's `parameters` field; it is TLS-encrypted
// in transit and never persisted in ARM because the action-style API creates
// no tracked resource.
type Config struct {
	SubscriptionID string `json:"subscriptionId"`
	ResourceGroup  string `json:"resourceGroup"`
	Location       string `json:"location"`
	TenantID       string `json:"tenantId"`
	ResourceName   string `json:"resourceName"`
	VMID           string `json:"vmId"`
	PrivateKey     string `json:"privateKey"` //nolint:gosec // G117: this struct intentionally carries the private key; it is the in-VM script's input schema, never logged or persisted.
}

// BuildConfigJSON serializes the config and returns it base64-encoded so it
// can be passed as a single runCommand parameter value.
func BuildConfigJSON(c Config) (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// For returns the embedded script body and the runCommand parameter name to
// use for that script, given an OS type. osType may be "Linux", "Windows", or
// any case variation thereof.
//
// On Linux, the `Microsoft.Compute/virtualMachines/runCommands` handler
// exposes each declared parameter to the script as an environment variable
// named after the parameter (parameter name "configB64" → `$configB64`). The
// .sh script reads the env var directly.
//
// On Windows, the same handler invokes PowerShell with named parameters, so
// the parameter name must match the script's `param([string]$Config)`
// declaration exactly.
func For(osType string) (body, paramName string) {
	switch strings.ToLower(osType) {
	case "windows":
		return windowsScript, "Config"
	default:
		return linuxScript, "configB64"
	}
}
