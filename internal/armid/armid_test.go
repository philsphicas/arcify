package armid

import "testing"

func TestParseVM_OK(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantSub  string
		wantRG   string
		wantName string
	}{
		{
			name:     "canonical",
			in:       "/subscriptions/abc-123/resourceGroups/my-rg/providers/Microsoft.Compute/virtualMachines/my-vm",
			wantSub:  "abc-123",
			wantRG:   "my-rg",
			wantName: "my-vm",
		},
		{
			name:     "trailing slash trimmed",
			in:       "/subscriptions/abc/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/vm/",
			wantSub:  "abc",
			wantRG:   "rg",
			wantName: "vm",
		},
		{
			name:     "case-insensitive segment keys, value casing preserved",
			in:       "/SUBSCRIPTIONS/MixedCaseSub/RESOURCEGROUPS/MixedRG/PROVIDERS/microsoft.compute/VIRTUALMACHINES/MyVM",
			wantSub:  "MixedCaseSub",
			wantRG:   "MixedRG",
			wantName: "MyVM",
		},
		{
			name:     "leading/trailing whitespace tolerated",
			in:       "  /subscriptions/x/resourceGroups/y/providers/Microsoft.Compute/virtualMachines/z  ",
			wantSub:  "x",
			wantRG:   "y",
			wantName: "z",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseVM(tc.in)
			if err != nil {
				t.Fatalf("ParseVM(%q) returned error: %v", tc.in, err)
			}
			if got.SubscriptionID != tc.wantSub {
				t.Errorf("SubscriptionID = %q, want %q", got.SubscriptionID, tc.wantSub)
			}
			if got.ResourceGroup != tc.wantRG {
				t.Errorf("ResourceGroup = %q, want %q", got.ResourceGroup, tc.wantRG)
			}
			if got.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tc.wantName)
			}
			if got.ARMID == "" {
				t.Errorf("ARMID is empty; want canonicalized id")
			}
		})
	}
}

func TestParseVM_Errors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"no leading slash", "subscriptions/a/resourceGroups/b/providers/Microsoft.Compute/virtualMachines/c"},
		{"too few segments", "/subscriptions/a/resourceGroups/b"},
		{"too many segments", "/subscriptions/a/resourceGroups/b/providers/Microsoft.Compute/virtualMachines/c/extensions/x"},
		{"wrong root segment", "/foo/a/resourceGroups/b/providers/Microsoft.Compute/virtualMachines/c"},
		{"wrong provider", "/subscriptions/a/resourceGroups/b/providers/Microsoft.Network/virtualMachines/c"},
		{"wrong resource type", "/subscriptions/a/resourceGroups/b/providers/Microsoft.Compute/networkInterfaces/c"},
		{"empty subscription", "/subscriptions//resourceGroups/b/providers/Microsoft.Compute/virtualMachines/c"},
		{"empty RG", "/subscriptions/a/resourceGroups//providers/Microsoft.Compute/virtualMachines/c"},
		{"empty VM name", "/subscriptions/a/resourceGroups/b/providers/Microsoft.Compute/virtualMachines/"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseVM(tc.in)
			if err == nil {
				t.Fatalf("ParseVM(%q) = %+v, want error", tc.in, got)
			}
		})
	}
}
