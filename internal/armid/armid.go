// Package armid parses Azure Resource Manager resource IDs that arcify cares about.
package armid

import (
	"fmt"
	"strings"
)

// VMResource identifies an Azure virtual machine.
type VMResource struct {
	ARMID          string // canonical, normalized to /subscriptions/.../virtualMachines/<name>
	SubscriptionID string
	ResourceGroup  string
	Name           string
}

// ParseVM parses a VM ARM ID such as:
//
//	/subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.Compute/virtualMachines/<name>
//
// Matching of segment keys (subscriptions, resourceGroups, providers, etc.) is
// case-insensitive (per ARM convention); resource values preserve their input
// casing.
func ParseVM(id string) (*VMResource, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("empty ARM ID")
	}
	// Trim a single trailing slash.
	id = strings.TrimRight(id, "/")

	if !strings.HasPrefix(id, "/") {
		return nil, fmt.Errorf("ARM ID must start with '/': %q", id)
	}

	parts := strings.Split(id[1:], "/")
	// Expect exactly: subscriptions / <sub> / resourceGroups / <rg> /
	//                 providers / Microsoft.Compute / virtualMachines / <name>
	if len(parts) != 8 {
		return nil, fmt.Errorf("VM ARM ID must have 8 segments, got %d: %q", len(parts), id)
	}
	if !strings.EqualFold(parts[0], "subscriptions") {
		return nil, fmt.Errorf("expected first segment %q, got %q", "subscriptions", parts[0])
	}
	if !strings.EqualFold(parts[2], "resourceGroups") {
		return nil, fmt.Errorf("expected third segment %q, got %q", "resourceGroups", parts[2])
	}
	if !strings.EqualFold(parts[4], "providers") {
		return nil, fmt.Errorf("expected fifth segment %q, got %q", "providers", parts[4])
	}
	if !strings.EqualFold(parts[5], "Microsoft.Compute") {
		return nil, fmt.Errorf("expected provider Microsoft.Compute; got %s", parts[5])
	}
	if !strings.EqualFold(parts[6], "virtualMachines") {
		return nil, fmt.Errorf("expected resource type virtualMachines; got %s", parts[6])
	}

	sub := parts[1]
	rg := parts[3]
	name := parts[7]

	if sub == "" || rg == "" || name == "" {
		return nil, fmt.Errorf("subscription, resource group, and VM name must all be non-empty (got %q/%q/%q)", sub, rg, name)
	}

	return &VMResource{
		ARMID:          "/subscriptions/" + sub + "/resourceGroups/" + rg + "/providers/Microsoft.Compute/virtualMachines/" + name,
		SubscriptionID: sub,
		ResourceGroup:  rg,
		Name:           name,
	}, nil
}
