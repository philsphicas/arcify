package arm

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
)

// VMClient wraps armcompute.VirtualMachinesClient with the small surface
// arcify needs.
type VMClient struct {
	inner *armcompute.VirtualMachinesClient
}

// Get fetches a VM and extracts the OS type, location, and guest-agent state.
func (c *VMClient) Get(ctx context.Context, rg, name string) (*VMInfo, error) {
	resp, err := c.inner.Get(ctx, rg, name, nil)
	if err != nil {
		return nil, err
	}
	vm := resp.VirtualMachine

	info := &VMInfo{}

	if vm.Location != nil {
		info.Location = *vm.Location
	}

	// Determine OS type with a sequence of fallbacks.
	if vm.Properties != nil {
		if vm.Properties.StorageProfile != nil &&
			vm.Properties.StorageProfile.OSDisk != nil &&
			vm.Properties.StorageProfile.OSDisk.OSType != nil {
			info.OSType = string(*vm.Properties.StorageProfile.OSDisk.OSType)
		}
		if info.OSType == "" && vm.Properties.OSProfile != nil {
			if vm.Properties.OSProfile.LinuxConfiguration != nil {
				info.OSType = "Linux"
			} else if vm.Properties.OSProfile.WindowsConfiguration != nil {
				info.OSType = "Windows"
			}
		}

		// provisionVMAgent: presence of the guest agent. Default true when unset.
		info.ProvisionVMAgent = true
		if vm.Properties.OSProfile != nil {
			if cfg := vm.Properties.OSProfile.LinuxConfiguration; cfg != nil && cfg.ProvisionVMAgent != nil {
				info.ProvisionVMAgent = *cfg.ProvisionVMAgent
			}
			if cfg := vm.Properties.OSProfile.WindowsConfiguration; cfg != nil && cfg.ProvisionVMAgent != nil {
				info.ProvisionVMAgent = *cfg.ProvisionVMAgent
			}
			// AllowExtensionOperations gates runCommand specifically.
			if vm.Properties.OSProfile.AllowExtensionOperations != nil &&
				!*vm.Properties.OSProfile.AllowExtensionOperations {
				info.ProvisionVMAgent = false
			}
		}
	}

	if info.OSType == "" {
		return nil, fmt.Errorf("could not determine OS type from VM properties")
	}
	if info.Location == "" {
		return nil, fmt.Errorf("VM has no location field")
	}
	return info, nil
}
