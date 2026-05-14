package scripts

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildConfigJSON_Roundtrip(t *testing.T) {
	in := Config{
		SubscriptionID: "sub-1",
		ResourceGroup:  "rg-1",
		Location:       "eastus",
		TenantID:       "tenant-1",
		ResourceName:   "arc-1",
		VMID:           "vmid-1",
		PrivateKey:     "AAAA",
	}
	b64, err := BuildConfigJSON(in)
	if err != nil {
		t.Fatalf("BuildConfigJSON: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	var out Config
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("roundtrip mismatch\nin:  %+v\nout: %+v", in, out)
	}
}

func TestFor_OSDispatch(t *testing.T) {
	body, param := For("Linux")
	if !strings.HasPrefix(body, "#!/bin/bash") {
		t.Errorf("Linux: expected shebang, got prefix %q", body[:min(20, len(body))])
	}
	if param != "configB64" {
		t.Errorf("Linux paramName = %q, want %q", param, "configB64")
	}
	if !strings.Contains(body, "ARCIFY_RESULT=success") {
		t.Error("Linux script missing ARCIFY_RESULT=success sentinel")
	}

	body, param = For("Windows")
	if !strings.Contains(body, "param(") {
		t.Errorf("Windows: expected param() block, got prefix %q", body[:min(40, len(body))])
	}
	if param != "Config" {
		t.Errorf("Windows paramName = %q, want %q", param, "Config")
	}
	if !strings.Contains(body, "ARCIFY_RESULT=success") {
		t.Error("Windows script missing ARCIFY_RESULT=success sentinel")
	}

	// Default falls through to Linux.
	body, _ = For("")
	if !strings.HasPrefix(body, "#!/bin/bash") {
		t.Errorf("empty OS should default to Linux; got prefix %q", body[:min(20, len(body))])
	}
}
