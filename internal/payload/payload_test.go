package payload

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func samplePayload() Payload {
	return Payload{
		ArcResourceID:  "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.HybridCompute/machines/vm1",
		SubscriptionID: "sub",
		ResourceGroup:  "rg",
		Name:           "vm1",
		Location:       "eastus2",
		TenantID:       "tid",
		VMID:           "vmid-uuid",
		PrivateKey:     "BASE64=KEY+VALUE/with-special=chars",
	}
}

func TestFormatEnv_ContainsAllKeys(t *testing.T) {
	var buf bytes.Buffer
	if err := FormatEnv(&buf, samplePayload()); err != nil {
		t.Fatalf("FormatEnv: %v", err)
	}
	out := buf.String()
	wantKeys := []string{
		"ARC_RESOURCE_ID=",
		"ARC_SUBSCRIPTION_ID=",
		"ARC_RESOURCE_GROUP=",
		"ARC_RESOURCE_NAME=",
		"ARC_LOCATION=",
		"ARC_TENANT_ID=",
		"ARC_VMID=",
		"ARC_PRIVATE_KEY=",
	}
	for _, k := range wantKeys {
		if !strings.Contains(out, k) {
			t.Errorf("output missing key %q. full output:\n%s", k, out)
		}
	}
	if !strings.Contains(out, "# arcify --precreate payload") {
		t.Errorf("output missing header comment. full output:\n%s", out)
	}
}

func TestFormatEnv_NoQuoting(t *testing.T) {
	var buf bytes.Buffer
	if err := FormatEnv(&buf, samplePayload()); err != nil {
		t.Fatalf("FormatEnv: %v", err)
	}
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(line, "ARC_PRIVATE_KEY=") {
			val := strings.TrimPrefix(line, "ARC_PRIVATE_KEY=")
			if val != "BASE64=KEY+VALUE/with-special=chars" {
				t.Errorf("ARC_PRIVATE_KEY value got quoted/escaped: %q", val)
			}
			return
		}
	}
	t.Fatal("ARC_PRIVATE_KEY line not found")
}

func TestFormatEnv_RejectsNewlines(t *testing.T) {
	p := samplePayload()
	p.Name = "vm\nwith-newline"
	var buf bytes.Buffer
	err := FormatEnv(&buf, p)
	if err == nil {
		t.Fatal("expected error for newline-containing value, got nil")
	}
	if !strings.Contains(err.Error(), "ARC_RESOURCE_NAME") {
		t.Errorf("error should mention the field, got: %v", err)
	}
}

func TestFormatEnv_WriteError(t *testing.T) {
	err := FormatEnv(errorWriter{}, samplePayload())
	if err == nil {
		t.Fatal("expected error from a failing writer, got nil")
	}
}

func TestFormatJSON_ParsesBack(t *testing.T) {
	var buf bytes.Buffer
	p := samplePayload()
	if err := FormatJSON(&buf, p); err != nil {
		t.Fatalf("FormatJSON: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput:\n%s", err, buf.String())
	}
	want := map[string]string{
		"arcResourceId":  p.ArcResourceID,
		"subscriptionId": p.SubscriptionID,
		"resourceGroup":  p.ResourceGroup,
		"name":           p.Name,
		"location":       p.Location,
		"tenantId":       p.TenantID,
		"vmId":           p.VMID,
		"privateKey":     p.PrivateKey,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("field %q = %q, want %q", k, got[k], v)
		}
	}
}

func TestFormatJSON_WriteError(t *testing.T) {
	err := FormatJSON(errorWriter{}, samplePayload())
	if err == nil {
		t.Fatal("expected error from a failing writer, got nil")
	}
}

type errorWriter struct{}

func (errorWriter) Write(_ []byte) (int, error) { return 0, errors.New("simulated write failure") }
