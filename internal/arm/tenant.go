package arm

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
)

// TenantClient resolves the tenant ID for a subscription via the Subscriptions
// GET endpoint.
type TenantClient struct {
	inner *armsubscriptions.Client
}

// Get returns the tenant ID that owns the subscription.
func (c *TenantClient) Get(ctx context.Context, subID string) (string, error) {
	resp, err := c.inner.Get(ctx, subID, nil)
	if err != nil {
		return "", err
	}
	if resp.TenantID == nil || *resp.TenantID == "" {
		return "", fmt.Errorf("subscription %s returned no tenantId", subID)
	}
	return *resp.TenantID, nil
}
