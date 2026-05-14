package main

import (
	"flag"
	"reflect"
	"strings"
	"testing"
)

func newTestFS() *flag.FlagSet {
	fs := flag.NewFlagSet("arcify", flag.ContinueOnError)
	var s string
	var b bool
	fs.StringVar(&s, "arc-subscription", "", "")
	fs.StringVar(&s, "arc-rg", "", "")
	fs.StringVar(&s, "arc-name", "", "")
	fs.StringVar(&s, "arc-location", "", "")
	fs.StringVar(&s, "arc-tenant", "", "")
	fs.StringVar(&s, "tags", "", "")
	fs.StringVar(&s, "wait", "", "")
	fs.StringVar(&s, "output", "", "")
	fs.BoolVar(&b, "no-wait", false, "")
	fs.BoolVar(&b, "force", false, "")
	fs.BoolVar(&b, "dry-run", false, "")
	fs.BoolVar(&b, "verbose", false, "")
	fs.BoolVar(&b, "v", false, "")
	fs.BoolVar(&b, "version", false, "")
	fs.BoolVar(&b, "precreate", false, "")
	return fs
}

func TestReorderArgs(t *testing.T) {
	fs := newTestFS()
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "flags-only-before",
			in:   []string{"--dry-run", "--arc-rg", "myrg", "/sub/x"},
			want: []string{"--dry-run", "--arc-rg", "myrg", "/sub/x"},
		},
		{
			name: "positional-first-bool-after",
			in:   []string{"/sub/x", "--dry-run"},
			want: []string{"--dry-run", "/sub/x"},
		},
		{
			name: "positional-first-value-after",
			in:   []string{"/sub/x", "--arc-rg", "myrg"},
			want: []string{"--arc-rg", "myrg", "/sub/x"},
		},
		{
			name: "mixed-flag-positional-flag",
			in:   []string{"--force", "/sub/x", "--arc-name", "vm1"},
			want: []string{"--force", "--arc-name", "vm1", "/sub/x"},
		},
		{
			name: "equals-form-doesn't-consume-next",
			in:   []string{"/sub/x", "--arc-rg=myrg", "--force"},
			want: []string{"--arc-rg=myrg", "--force", "/sub/x"},
		},
		{
			name: "double-dash-stops-reorder",
			in:   []string{"/sub/x", "--", "--this-is-not-a-flag"},
			want: []string{"--", "--this-is-not-a-flag", "/sub/x"},
		},
		{
			name: "short-bool",
			in:   []string{"/sub/x", "-v"},
			want: []string{"-v", "/sub/x"},
		},
		{
			name: "no-wait-bool-after-positional",
			in:   []string{"/sub/x", "--no-wait"},
			want: []string{"--no-wait", "/sub/x"},
		},
		{
			name: "wait-duration-after-positional",
			in:   []string{"/sub/x", "--wait", "10m"},
			want: []string{"--wait", "10m", "/sub/x"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := reorderArgs(tc.in, fs)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("reorderArgs(%v) =\n  %v\nwant\n  %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseFlags_PositionalAfterFlags(t *testing.T) {
	opts, err := parseFlags([]string{"/sub/x", "--arc-rg", "myrg", "--dry-run"}, nopWriter{})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.vmARMID != "/sub/x" {
		t.Errorf("vmARMID = %q, want /sub/x", opts.vmARMID)
	}
	if opts.arcRG != "myrg" {
		t.Errorf("arcRG = %q, want myrg", opts.arcRG)
	}
	if !opts.dryRun {
		t.Error("dryRun should be true")
	}
}

func TestParseFlags_PositionalFirst(t *testing.T) {
	opts, err := parseFlags([]string{"--arc-rg", "myrg", "/sub/x"}, nopWriter{})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.vmARMID != "/sub/x" {
		t.Errorf("vmARMID = %q, want /sub/x", opts.vmARMID)
	}
	if opts.arcRG != "myrg" {
		t.Errorf("arcRG = %q, want myrg", opts.arcRG)
	}
}

func TestParseFlags_Missing(t *testing.T) {
	if _, err := parseFlags([]string{"--dry-run"}, nopWriter{}); err == nil {
		t.Error("expected error for missing positional")
	}
}

func TestParseFlags_DefaultWait(t *testing.T) {
	opts, err := parseFlags([]string{"/sub/x"}, nopWriter{})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.wait.Minutes() != 5 {
		t.Errorf("default --wait = %s, want 5m", opts.wait)
	}
	if opts.noWait {
		t.Error("--no-wait should default to false")
	}
}

func TestParseFlags_NegativeWaitRejected(t *testing.T) {
	if _, err := parseFlags([]string{"/sub/x", "--wait", "-1m"}, nopWriter{}); err == nil {
		t.Error("expected error for negative --wait")
	}
}

func TestParseFlags_Precreate_OK(t *testing.T) {
	opts, err := parseFlags([]string{
		"--precreate",
		"--arc-subscription", "sub",
		"--arc-rg", "rg",
		"--arc-name", "name",
		"--arc-location", "eastus2",
	}, nopWriter{})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !opts.precreate {
		t.Error("precreate should be true")
	}
	if opts.output != "env" {
		t.Errorf("default output = %q, want env", opts.output)
	}
	if opts.vmARMID != "" {
		t.Errorf("vmARMID should be empty in precreate mode, got %q", opts.vmARMID)
	}
}

func TestParseFlags_Precreate_RejectsPositional(t *testing.T) {
	_, err := parseFlags([]string{
		"--precreate",
		"--arc-subscription", "sub",
		"--arc-rg", "rg",
		"--arc-name", "name",
		"--arc-location", "eastus2",
		"/subscriptions/x/vm",
	}, nopWriter{})
	if err == nil {
		t.Fatal("expected error when --precreate is given a positional, got nil")
	}
	if !strings.Contains(err.Error(), "positional") {
		t.Errorf("error should mention positional, got: %v", err)
	}
}

func TestParseFlags_Precreate_RequiresArcFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"missing sub", []string{"--precreate", "--arc-rg", "rg", "--arc-name", "n", "--arc-location", "l"}},
		{"missing rg", []string{"--precreate", "--arc-subscription", "s", "--arc-name", "n", "--arc-location", "l"}},
		{"missing name", []string{"--precreate", "--arc-subscription", "s", "--arc-rg", "rg", "--arc-location", "l"}},
		{"missing location", []string{"--precreate", "--arc-subscription", "s", "--arc-rg", "rg", "--arc-name", "n"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseFlags(tc.args, nopWriter{}); err == nil {
				t.Errorf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestParseFlags_Precreate_NoWaitRejected(t *testing.T) {
	_, err := parseFlags([]string{
		"--precreate", "--no-wait",
		"--arc-subscription", "s", "--arc-rg", "rg",
		"--arc-name", "n", "--arc-location", "l",
	}, nopWriter{})
	if err == nil {
		t.Fatal("expected error: --no-wait with --precreate, got nil")
	}
}

func TestParseFlags_Precreate_BadOutput(t *testing.T) {
	_, err := parseFlags([]string{
		"--precreate", "--output", "yaml",
		"--arc-subscription", "s", "--arc-rg", "rg",
		"--arc-name", "n", "--arc-location", "l",
	}, nopWriter{})
	if err == nil {
		t.Fatal("expected error for unknown --output, got nil")
	}
}

func TestParseFlags_OutputRejectedWithoutPrecreate(t *testing.T) {
	_, err := parseFlags([]string{"/sub/x", "--output", "env"}, nopWriter{})
	if err == nil {
		t.Fatal("expected error: --output without --precreate")
	}
}

func TestParseFlags_ArcTenantRejectedWithoutPrecreate(t *testing.T) {
	_, err := parseFlags([]string{"/sub/x", "--arc-tenant", "tid"}, nopWriter{})
	if err == nil {
		t.Fatal("expected error: --arc-tenant without --precreate")
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
