package client

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/notes"
)

type policyCompletion struct {
	started     chan context.Context
	release     chan struct{}
	releaseOnce sync.Once
}

func (g *policyCompletion) unblock() {
	if g.release != nil {
		g.releaseOnce.Do(func() { close(g.release) })
	}
}

func (*policyCompletion) CloseIdleConnections() {}
func (*policyCompletion) CountInputTokens(context.Context, string, string) (int, error) {
	return 10, nil
}
func (*policyCompletion) Generate(context.Context, assistant.PackedContext, int) (assistant.ModelAnswer, error) {
	return assistant.ModelAnswer{}, errors.New("unexpected grounded generation")
}
func (g *policyCompletion) GenerateGeneral(ctx context.Context, _ string, _ int) (string, error) {
	g.started <- ctx
	if g.release != nil {
		<-g.release
	}
	// Ignore cancellation at the external provider boundary. The Client must
	// still prevent a result from the retired policy being published.
	return "Stale answer from retired provider", nil
}

func aiPolicyClient(t *testing.T, model *policyCompletion) *Client {
	t.Helper()
	controller := &localAIController{
		path: filepath.Join(t.TempDir(), "ai.json"), service: notes.NewService(nil, nil, nil),
		settings: LocalAISettings{Enabled: true, Provider: ProviderClaude, Model: "synthetic-model"}, askModel: model,
	}
	c := NewClient(&testData{}).WithLocalAI(controller)
	t.Cleanup(func() { model.unblock(); c.Close() })
	return c
}

func TestAIPolicyChangeCancelsActiveAskWithoutChangingMemoryEpoch(t *testing.T) {
	for _, wrapper := range []string{"ConfigureLocalAI", "ConfigureAI"} {
		t.Run(wrapper, func(t *testing.T) {
			model := &policyCompletion{started: make(chan context.Context, 1), release: make(chan struct{})}
			c := aiPolicyClient(t, model)
			epoch, stable := c.BeginMemoryRead()
			if !stable {
				t.Fatal("fixture memory is changing")
			}
			done := make(chan error, 1)
			go func() {
				result, err := c.AskMemory(context.Background(), "Explain compiler optimization")
				if result.Answer.Text != "" {
					done <- errors.New("retired policy published an answer")
					return
				}
				done <- err
			}()
			var inference context.Context
			select {
			case inference = <-model.started:
			case <-time.After(time.Second):
				t.Fatal("Ask did not reach synthetic provider")
			}
			var err error
			if wrapper == "ConfigureAI" {
				err = c.ConfigureAI(context.Background(), LocalAISettings{}, "")
			} else {
				err = c.ConfigureLocalAI(context.Background(), LocalAISettings{})
			}
			if err != nil {
				model.unblock()
				t.Fatal(err)
			}
			select {
			case <-inference.Done():
			case <-time.After(200 * time.Millisecond):
				model.unblock()
				<-done //nolint:errcheck // The test is already failing; drain the released operation.
				t.Fatal("model policy change did not cancel active inference")
			}
			model.unblock()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("retired Ask error=%v, want canceled", err)
			}
			if !c.MemoryReadCurrent(epoch) || c.MemoryIsChanging() {
				t.Fatal("model policy change invalidated encrypted memory epoch")
			}
			select {
			case <-c.MemoryChanges():
				t.Fatal("model policy emitted a memory mutation")
			default:
			}
		})
	}
}

func TestAIPolicyChangeCannotMissAskBetweenSnapshotAndRegistration(t *testing.T) {
	model := &policyCompletion{started: make(chan context.Context, 1), release: make(chan struct{})}
	c := aiPolicyClient(t, model)
	// Stop Ask at cancellation registration, after it has selected the old
	// service. Configuration must wait for that registration before cancelling.
	c.memoryChangeMu.Lock()
	held := true
	defer func() {
		if held {
			c.memoryChangeMu.Unlock()
		}
	}()
	asked := make(chan error, 1)
	go func() {
		result, err := c.AskMemory(context.Background(), "Explain compiler optimization")
		if result.Answer.Text != "" {
			asked <- errors.New("unregistered old service published an answer")
			return
		}
		asked <- err
	}()
	deadline := time.Now().Add(time.Second)
	for c.aiPolicyMu.TryLock() {

		c.aiPolicyMu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("Ask did not protect its provider snapshot")
		}
		runtime.Gosched()
	}
	configured := make(chan error, 1)
	go func() { configured <- c.ConfigureLocalAI(context.Background(), LocalAISettings{}) }()
	select {
	case err := <-configured:
		t.Fatalf("configuration bypassed old Ask registration: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	c.memoryChangeMu.Unlock()
	held = false
	if err := <-configured; err != nil {
		t.Fatal(err)
	}
	model.unblock()
	if err := <-asked; !errors.Is(err, context.Canceled) {
		t.Fatalf("old snapshot escaped cancellation: %v", err)
	}
}

func TestAIPolicyChangeBlocksOldServiceAdmissionUntilSettingsPublish(t *testing.T) {
	model := &policyCompletion{started: make(chan context.Context, 1)}
	c := aiPolicyClient(t, model)
	// Pause real configuration before publication, after its policy writer lock.
	c.localAI.configMu.Lock()
	held := true
	defer func() {
		if held {
			c.localAI.configMu.Unlock()
		}
	}()
	configured := make(chan error, 1)
	go func() { configured <- c.ConfigureLocalAI(context.Background(), LocalAISettings{}) }()
	deadline := time.Now().Add(time.Second)
	for c.aiPolicyMu.TryRLock() {

		c.aiPolicyMu.RUnlock()
		if time.Now().After(deadline) {
			t.Fatal("configuration did not fence Ask admission")
		}
		runtime.Gosched()
	}
	asked := make(chan error, 1)
	go func() { _, err := c.AskMemory(context.Background(), "Explain compiler optimization"); asked <- err }()
	select {
	case <-model.started:
		t.Fatal("Ask captured the old provider while configuration was publishing")
	case <-asked: //nolint:errcheck // Any early result fails this blocked-configuration test.
		t.Fatal("Ask bypassed pending configuration")
	case <-time.After(30 * time.Millisecond):
	}
	c.localAI.configMu.Unlock()
	held = false
	if err := <-configured; err != nil {
		t.Fatal(err)
	}
	if err := <-asked; !errors.Is(err, assistant.ErrAskUnavailable) {
		t.Fatalf("new Ask used retired service: %v", err)
	}
}

func TestAIPolicyConfigurationAfterClientCloseIsRejected(t *testing.T) {
	for _, wrapper := range []string{"ConfigureLocalAI", "ConfigureAI"} {
		t.Run(wrapper, func(t *testing.T) {
			c := aiPolicyClient(t, &policyCompletion{})
			before := c.LocalAISettings()
			c.Close()
			var err error
			if wrapper == "ConfigureAI" {
				err = c.ConfigureAI(context.Background(), LocalAISettings{}, "")
			} else {
				err = c.ConfigureLocalAI(context.Background(), LocalAISettings{})
			}
			if !errors.Is(err, ErrClientClosed) {
				t.Fatalf("configuration after Close returned %v, want client closed", err)
			}
			if c.LocalAISettings() != before {
				t.Fatal("closed client changed model configuration")
			}
		})
	}
}

type policySecrets struct {
	bootstrapSecrets
	started chan context.Context
	release chan struct{}
	once    sync.Once
}

func (s *policySecrets) unblock() { s.once.Do(func() { close(s.release) }) }
func (s *policySecrets) Put(ctx context.Context, _ string, _ []byte) error {
	s.started <- ctx
	<-s.release
	return ctx.Err()
}

func TestAIPolicyConfigurationIsCancelledAndDrainedByClientClose(t *testing.T) {
	c := aiPolicyClient(t, &policyCompletion{})
	secrets := &policySecrets{started: make(chan context.Context, 1), release: make(chan struct{})}
	c.localAI.secrets = secrets
	t.Cleanup(secrets.unblock)
	configured := make(chan error, 1)
	go func() {
		configured <- c.ConfigureAI(context.Background(), LocalAISettings{Enabled: true, Provider: ProviderClaude, Model: "synthetic-model"}, "SYNTHETIC_PRIVATE_KEY")
	}()
	var protectedOperation context.Context
	select {
	case protectedOperation = <-secrets.started:
	case <-time.After(time.Second):
		t.Fatal("configuration did not reach synthetic protected storage")
	}
	if _, bounded := protectedOperation.Deadline(); !bounded {
		secrets.unblock()
		<-configured //nolint:errcheck // The test is already failing; drain the released configuration.
		t.Fatal("configuration protected-storage context is unbounded")
	}
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	select {
	case <-protectedOperation.Done():
	case <-closed:
		t.Fatal("Close returned before cancelling active configuration")
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel active configuration")
	}
	select {
	case <-closed:
		t.Fatal("Close did not drain active protected-storage operation")
	case <-time.After(30 * time.Millisecond):
	}
	secrets.unblock()
	if err := <-configured; err == nil {
		t.Fatal("cancelled configuration succeeded")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after configuration drained")
	}
}
