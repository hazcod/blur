package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/core"
	"github.com/hazcod/blur/internal/platform"
)

type countingClient struct{ called int }

func (c *countingClient) Status(context.Context) (core.StatusReport, error) {
	c.called++
	return core.StatusReport{}, nil
}
func (c *countingClient) Capabilities(context.Context) ([]platform.CapabilityReport, error) {
	c.called++
	return nil, nil
}

type largeStatusClient struct{ status core.StatusReport }

func (c largeStatusClient) Status(context.Context) (core.StatusReport, error) { return c.status, nil }
func (c largeStatusClient) Capabilities(context.Context) ([]platform.CapabilityReport, error) {
	return nil, nil
}

func TestRunStatusAndCapabilitiesUseAuthorizedClient(t *testing.T) {
	client, err := core.NewEphemeralDemoCLIClient()
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"status", "capabilities"} {
		var out, errOut bytes.Buffer
		if code := run([]string{command}, &out, &errOut, client); code != 0 {
			t.Fatalf("%s exit %d: %s", command, code, errOut.String())
		}
		if out.Len() == 0 || out.Len() > 65536 || errOut.Len() != 0 || !json.Valid(out.Bytes()) {
			t.Fatalf("%s invalid output", command)
		}
		if command == "status" && !strings.Contains(out.String(), `"mode":"ephemeral_memory"`) {
			t.Fatalf("status: %s", out.String())
		}
		if command == "capabilities" {
			var reports []platform.CapabilityReport
			if err := json.Unmarshal(out.Bytes(), &reports); err != nil || len(reports) != 10 {
				t.Fatalf("capabilities: %s, %v", out.String(), err)
			}
		}
	}
}

func TestRunRejectsUnknownAndMissingWithoutCallingClient(t *testing.T) {
	for _, args := range [][]string{nil, {""}, {"export"}, {"status", "extra"}} {
		client := &countingClient{}
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut, client); code == 0 || client.called != 0 || out.Len() != 0 || errOut.Len() == 0 || errOut.Len() > 128 {
			t.Fatalf("invalid command %q: exit %d, calls %d, output %q/%q", args, code, client.called, out.String(), errOut.String())
		}
	}
}

func TestBlurCoreEmitsOneOfflineStatusDocumentAndExits(t *testing.T) {
	cmd := exec.Command("go", "run", "../blur-core")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var status core.StatusReport
	if err := json.Unmarshal(out, &status); err != nil {
		t.Fatalf("startup JSON: %v", err)
	}
	if status.Mode != "ephemeral_memory" || status.EncryptedAtRest || status.AIEnabled || status.NetworkListening || !status.Ready {
		t.Fatalf("startup status: %+v", status)
	}
}

func TestRunAccountsForFinalNewlineInOutputCap(t *testing.T) {
	status := core.StatusReport{}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	status.Version = strings.Repeat("x", 65536-len(encoded))
	var out, errOut bytes.Buffer
	if code := run([]string{"status"}, &out, &errOut, largeStatusClient{status}); code == 0 || out.Len() != 0 || errOut.Len() == 0 {
		t.Fatalf("oversized document emitted: exit %d, bytes %d", code, out.Len())
	}
}

func TestUnknownCommandDoesNotIssueDemoGrant(t *testing.T) {
	called := false
	var out, errOut bytes.Buffer
	code := launch([]string{"export"}, &out, &errOut, func() (client, error) {
		called = true
		return &countingClient{}, nil
	})
	if code == 0 || called || out.Len() != 0 {
		t.Fatalf("unknown command issued a grant: code=%d called=%t", code, called)
	}
}
