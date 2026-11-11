//go:build darwin || linux

package browsercapture

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/localapi"
)

func TestUnixServerAcceptsAuthenticatedBoundedFrameAndStopsCleanly(t *testing.T) {
	now := time.Now().UTC()
	grants, _ := localapi.NewGrantStore(func() time.Time { return now }, nil, 8)
	grant, _ := grants.Issue("safari-extension", []localapi.Scope{localapi.ScopeCaptureIngest}, time.Hour)
	sink := &receiverSink{encrypted: true}
	receiver, _ := NewReceiver(grants, sink, func() bool { return true }, func() time.Time { return now })
	dir, err := os.MkdirTemp("/tmp", "blur-safari-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	_ = os.Chmod(dir, 0o700)
	path := filepath.Join(dir, "capture.sock")
	server, err := NewUnixServer(localapi.NewUnixTransport(MaxWireBytes), localapi.EndpointDescriptor{Kind: localapi.EndpointUnix, Address: path}, receiver)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o600 || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket not private: %v %v", info, err)
	}
	payload := []byte(`{"browser":"com.apple.safari","url":"https://example.test","text":"body","active":true,"privacy_known":true,"private":false,"captured_at":"` + now.Format(time.RFC3339Nano) + `"}`)
	digest := sha256.Sum256(payload)
	proof, _ := localapi.NewProof(grant, localapi.ScopeCaptureIngest, digest, now, nil)
	frame, _ := json.Marshal(struct {
		Version int             `json:"version"`
		Payload json.RawMessage `json:"payload"`
		Proof   struct {
			GrantID   string    `json:"grant_id"`
			ClientID  string    `json:"client_id"`
			Scope     string    `json:"scope"`
			Timestamp time.Time `json:"timestamp"`
			Nonce     string    `json:"nonce"`
			MAC       string    `json:"mac"`
		} `json:"proof"`
	}{Version: 1, Payload: payload, Proof: struct {
		GrantID   string    `json:"grant_id"`
		ClientID  string    `json:"client_id"`
		Scope     string    `json:"scope"`
		Timestamp time.Time `json:"timestamp"`
		Nonce     string    `json:"nonce"`
		MAC       string    `json:"mac"`
	}{grant.ID, grant.ClientID, string(localapi.ScopeCaptureIngest), now, base64.RawURLEncoding.EncodeToString(proof.Nonce), hex.EncodeToString(proof.MAC[:])}})
	client, err := localapi.NewUnixTransport(MaxWireBytes).Dial(ctx, localapi.EndpointDescriptor{Kind: localapi.EndpointUnix, Address: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Send(ctx, frame); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		sink.mu.Lock()
		count := len(sink.rows)
		sink.mu.Unlock()
		if count == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	sink.mu.Lock()
	count := len(sink.rows)
	sink.mu.Unlock()
	if count != 1 {
		t.Fatalf("accepted observations=%d", count)
	}
	if err := server.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("socket remains after stop: %v", err)
	}
}

func TestUnixServerParentCancellationRemovesEndpoint(t *testing.T) {
	now := time.Now().UTC()
	grants, err := localapi.NewGrantStore(func() time.Time { return now }, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	sink := &receiverSink{encrypted: true}
	receiver, err := NewReceiver(grants, sink, func() bool { return true }, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/tmp", "b-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	_ = os.Chmod(dir, 0o700)
	path := filepath.Join(dir, "s")
	endpoint := localapi.EndpointDescriptor{Kind: localapi.EndpointUnix, Address: path}
	server, err := NewUnixServer(localapi.NewUnixTransport(MaxWireBytes), endpoint, receiver)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("canceled server left socket path: %v", err)
	}
	if err := server.Stop(); err != nil {
		t.Fatal(err)
	}
}
