// Package arm wraps the small subset of Azure SDK clients arcify needs:
// virtual machines, hybrid compute (Arc) machines, and subscription/tenant
// lookup. The runCommand action is invoked via the raw VM client (see
// `VMRaw` below); arcify uses the action-style runCommand API
// (`POST .../virtualMachines/{vm}/runCommand`), which does not create a
// tracked ARM resource and is therefore not wrapped here.
package arm

import (
	"errors"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/hybridcompute/armhybridcompute"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
)

// ErrArcResourceConflict is returned by ArcMachineClient.CreateOrUpdate when
// the target resource already exists and Force was not set.
var ErrArcResourceConflict = errors.New("arc machine resource already exists")

// Clients bundles every ARM client arcify uses.
type Clients struct {
	VM         *VMClient
	Tenant     *TenantClient
	ArcMachine *ArcMachineClient

	// VMRaw is the underlying SDK client for the VM's subscription. Exposed
	// so the runner package can call BeginRunCommand directly — the
	// action-style runCommand API is not a tracked ARM resource and is not
	// worth a dedicated wrapper.
	VMRaw *armcompute.VirtualMachinesClient
}

// NewClients constructs the full set of ARM clients. vmSubscriptionID is used
// for VM lookup, runCommand dispatch, and tenant resolution. arcSubscriptionID
// is used for the Microsoft.HybridCompute/machines resource — when the user
// wants the Arc record to live in a different subscription than the VM, the
// two differ; otherwise they're the same value.
func NewClients(vmSubscriptionID, arcSubscriptionID string, cred azcore.TokenCredential) (*Clients, error) {
	vmRaw, err := armcompute.NewVirtualMachinesClient(vmSubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("VirtualMachinesClient: %w", err)
	}
	hcRaw, err := armhybridcompute.NewMachinesClient(arcSubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("hybridcompute MachinesClient: %w", err)
	}
	subRaw, err := armsubscriptions.NewClient(cred, nil)
	if err != nil {
		return nil, fmt.Errorf("subscriptions Client: %w", err)
	}
	return &Clients{
		VM:         &VMClient{inner: vmRaw},
		Tenant:     &TenantClient{inner: subRaw},
		ArcMachine: &ArcMachineClient{inner: hcRaw},
		VMRaw:      vmRaw,
	}, nil
}

// ---- types ----

// CreateArcMachineInput describes the fields arcify needs to set on an Arc
// machine resource at create time.
type CreateArcMachineInput struct {
	ResourceGroup string
	Name          string
	Location      string
	Tags          map[string]string
	PublicKeyB64  string
	VMID          string
	Force         bool // delete-then-recreate on conflict
}

// VMInfo is the minimal subset of VM properties arcify needs.
type VMInfo struct {
	OSType           string // "Linux" or "Windows"
	Location         string
	ProvisionVMAgent bool
}

// ArcStatus is the subset of Arc machine state arcify reports back.
type ArcStatus struct {
	Status       string
	AgentVersion string
}
