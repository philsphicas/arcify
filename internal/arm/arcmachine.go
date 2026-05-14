package arm

import (
	"context"
	"errors"
	"net/http"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/hybridcompute/armhybridcompute"
)

// ArcMachineClient wraps armhybridcompute.MachinesClient.
type ArcMachineClient struct {
	inner *armhybridcompute.MachinesClient
}

// CreateOrUpdate writes a Microsoft.HybridCompute/machines resource.
//
// If a resource already exists at the target path:
//   - in.Force == false → returns ErrArcResourceConflict (caller decides what to do).
//   - in.Force == true  → deletes the existing resource, then re-creates.
//
// arcify never auto-rolls-back. Once this returns nil, the resource is the
// caller's to manage; on subsequent failures the resource stays in ARM and the
// caller is expected to surface its ARM ID (arcify does so via the
// "left Arc machine X behind" reminder printed in run()).
func (c *ArcMachineClient) CreateOrUpdate(ctx context.Context, in CreateArcMachineInput) error {
	existing, getErr := c.inner.Get(ctx, in.ResourceGroup, in.Name, nil)
	switch {
	case getErr == nil && existing.ID != nil:
		if !in.Force {
			return ErrArcResourceConflict
		}
		if _, derr := c.inner.Delete(ctx, in.ResourceGroup, in.Name, nil); derr != nil {
			return derr
		}
	case getErr != nil && !isNotFound(getErr):
		return getErr
	}

	body := armhybridcompute.Machine{
		Location: ptr(in.Location),
		Identity: &armhybridcompute.Identity{
			Type: ptr("SystemAssigned"),
		},
		Properties: &armhybridcompute.MachineProperties{
			ClientPublicKey: ptr(in.PublicKeyB64),
			VMID:            ptr(in.VMID),
		},
	}
	if len(in.Tags) > 0 {
		body.Tags = make(map[string]*string, len(in.Tags))
		for k, v := range in.Tags {
			v := v
			body.Tags[k] = &v
		}
	}

	if _, err := c.inner.CreateOrUpdate(ctx, in.ResourceGroup, in.Name, body, nil); err != nil {
		return err
	}
	return nil
}

// Get returns the raw SDK response for the Arc machine resource. The Machine
// embedded in the response carries the standard ARM envelope (id, name,
// type, location, properties, tags). Used by the runner package to poll
// status during VerifyConnected.
func (c *ArcMachineClient) Get(ctx context.Context, rg, name string) (armhybridcompute.MachinesClientGetResponse, error) {
	return c.inner.Get(ctx, rg, name, nil)
}

func ptr[T any](v T) *T { return &v }

func isNotFound(err error) bool {
	var rerr *azcore.ResponseError
	if errors.As(err, &rerr) {
		return rerr.StatusCode == http.StatusNotFound
	}
	return false
}
