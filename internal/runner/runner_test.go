package runner

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
)

func sp(s string) *string { return &s }

func TestSplitStdoutStderr(t *testing.T) {
	cases := []struct {
		name       string
		msg        string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "both markers present",
			msg:        "Enable succeeded: \n[stdout]\nhello\nworld\n\n[stderr]\noh no\n",
			wantStdout: "hello\nworld",
			wantStderr: "oh no",
		},
		{
			name:       "stdout only",
			msg:        "Enable succeeded: \n[stdout]\njust stdout\n",
			wantStdout: "just stdout",
			wantStderr: "",
		},
		{
			name:       "stderr only",
			msg:        "Enable succeeded: \n[stderr]\nuh oh\n",
			wantStdout: "",
			wantStderr: "uh oh",
		},
		{
			name:       "no markers (fall back to whole-message-as-stdout)",
			msg:        "random text without markers",
			wantStdout: "random text without markers",
			wantStderr: "",
		},
		{
			name:       "stdout body contains literal [stderr] inside a line (anchored marker not matched)",
			msg:        "Enable succeeded: \n[stdout]\nfound a [stderr] inline\n\n[stderr]\nreal stderr\n",
			wantStdout: "found a [stderr] inline",
			wantStderr: "real stderr",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			gotOut, gotErr := splitStdoutStderr(tc.msg)
			if gotOut != tc.wantStdout {
				t.Errorf("stdout = %q, want %q", gotOut, tc.wantStdout)
			}
			if gotErr != tc.wantStderr {
				t.Errorf("stderr = %q, want %q", gotErr, tc.wantStderr)
			}
		})
	}
}

func TestParseResult_SuccessSentinel(t *testing.T) {
	r := &armcompute.RunCommandResult{
		Value: []*armcompute.InstanceViewStatus{
			{Message: sp("Enable succeeded: \n[stdout]\n[arcify] connect ok\nARCIFY_RESULT=success\n\n[stderr]\n")},
		},
	}
	got := parseResult(r)
	if got == nil {
		t.Fatal("parseResult returned nil")
	}
	if !got.Succeeded {
		t.Error("expected Succeeded=true when sentinel present in stdout")
	}
	if got.Stderr != "" {
		t.Errorf("Stderr = %q, want empty", got.Stderr)
	}
}

func TestParseResult_NoSentinel(t *testing.T) {
	r := &armcompute.RunCommandResult{
		Value: []*armcompute.InstanceViewStatus{
			{Message: sp("Enable succeeded: \n[stdout]\nazcmagent connect failed\n\n[stderr]\nsome error\n")},
		},
	}
	got := parseResult(r)
	if got == nil {
		t.Fatal("parseResult returned nil")
	}
	if got.Succeeded {
		t.Error("expected Succeeded=false when sentinel absent")
	}
	if got.Stderr != "some error" {
		t.Errorf("Stderr = %q, want %q", got.Stderr, "some error")
	}
}

func TestParseResult_NilInput(t *testing.T) {
	if got := parseResult(nil); got != nil {
		t.Errorf("parseResult(nil) = %+v, want nil", got)
	}
}

func TestParseResult_SentinelNotOnLastLine(t *testing.T) {
	// Sentinel appears in the middle of stdout, but the last non-empty
	// line is something else — must not be classified as success.
	r := &armcompute.RunCommandResult{
		Value: []*armcompute.InstanceViewStatus{
			{Message: sp("Enable succeeded: \n[stdout]\nARCIFY_RESULT=success\nlater output line\n\n[stderr]\n")},
		},
	}
	got := parseResult(r)
	if got == nil {
		t.Fatal("parseResult returned nil")
	}
	if got.Succeeded {
		t.Error("expected Succeeded=false when sentinel is not the last non-empty line of stdout")
	}
}

func TestParseResult_SentinelFallbackWhenNoMarkers(t *testing.T) {
	// No [stdout]/[stderr] markers parsed → both extracted streams empty,
	// fall back to a substring search on the combined message.
	r := &armcompute.RunCommandResult{
		Value: []*armcompute.InstanceViewStatus{
			{Message: sp("ARCIFY_RESULT=success")},
		},
	}
	got := parseResult(r)
	if got == nil {
		t.Fatal("parseResult returned nil")
	}
	if !got.Succeeded {
		t.Error("expected Succeeded=true via combined-message fallback when marker parsing yields nothing")
	}
}

func TestIsLastNonEmptyLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"exact match", "ARCIFY_RESULT=success", true},
		{"trailing newline", "ARCIFY_RESULT=success\n", true},
		{"trailing newlines + spaces", "ARCIFY_RESULT=success\n  \n\n", true},
		{"trailing CRLF", "ARCIFY_RESULT=success\r\n", true},
		{"preceded by other lines", "doing stuff\nmore stuff\nARCIFY_RESULT=success\n", true},
		{"sentinel not on last line", "ARCIFY_RESULT=success\nlater output\n", false},
		{"sentinel substring on last line", "prefix ARCIFY_RESULT=success", false},
		{"empty", "", false},
		{"only whitespace", "  \n\t\n", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := isLastNonEmptyLine(tc.in, "ARCIFY_RESULT=success")
			if got != tc.want {
				t.Errorf("isLastNonEmptyLine(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestCommandIDFor(t *testing.T) {
	cases := map[string]string{
		"Linux":   "RunShellScript",
		"linux":   "RunShellScript",
		"Windows": "RunPowerShellScript",
		"WINDOWS": "RunPowerShellScript",
		"":        "RunShellScript",
		"weird":   "RunShellScript",
	}
	for in, want := range cases {
		if got := commandIDFor(in); got != want {
			t.Errorf("commandIDFor(%q) = %q, want %q", in, got, want)
		}
	}
}
