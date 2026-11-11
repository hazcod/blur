package client

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/platform"
)

func TestOpenLocalAppCreatesPrivateStoreAndReopensIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Blur")
	secrets := &bootstrapSecrets{values: make(map[string][]byte)}
	app, err := openLocalAppAt(context.Background(), dir, secrets, rand.Reader)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if !app.Client.CanAddNote() {
		t.Fatal("encrypted note service is not configured")
	}
	if !app.Client.CanDeleteSource() {
		t.Fatal("encrypted source deletion is not configured")
	}
	if err := app.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("data directory mode = %v, err=%v", info, err)
	}
	fileInfo, err := os.Stat(filepath.Join(dir, "memory.db"))
	if err != nil || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("database mode = %v, err=%v", fileInfo, err)
	}
	app, err = openLocalAppAt(context.Background(), dir, secrets, rand.Reader)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer app.Close()
}

func TestOpenLocalAppEnforcesSingleInstanceUntilClose(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Blur")
	secrets := &bootstrapSecrets{values: make(map[string][]byte)}
	first, err := openLocalAppAt(context.Background(), dir, secrets, rand.Reader)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	previous := bootstrapLockTimeout
	bootstrapLockTimeout = 100 * time.Millisecond
	defer func() { bootstrapLockTimeout = previous }()
	if _, err := openLocalAppAt(context.Background(), dir, secrets, rand.Reader); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("concurrent open error = %v, want ErrAlreadyRunning", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first app: %v", err)
	}
	second, err := openLocalAppAt(context.Background(), dir, secrets, rand.Reader)
	if err != nil {
		t.Fatalf("open after close: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close second app: %v", err)
	}
}

func TestOpenLocalAppPreservesSafeKeychainFailureState(t *testing.T) {
	secrets := &bootstrapSecrets{
		values: make(map[string][]byte),
		getErr: &platform.SecretStoreError{State: platform.SecretLocked},
	}
	_, err := openLocalAppAt(context.Background(), filepath.Join(t.TempDir(), "Blur"), secrets, rand.Reader)
	var state *platform.SecretStoreError
	if !errors.As(err, &state) || state.State != platform.SecretLocked {
		t.Fatalf("startup error = %v, want safe Keychain state locked", err)
	}
}

func TestOpenLocalAppReportsCorruptStoreWithoutRewritingIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Blur")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "memory.db")
	original := []byte("corrupt encrypted store fixture")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	secrets := &bootstrapSecrets{values: make(map[string][]byte)}
	_, err := openLocalAppAt(context.Background(), dir, secrets, rand.Reader)
	if !errors.Is(err, ErrEncryptedStoreCorrupt) {
		t.Fatalf("open error = %v, want corrupt-store classification", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != string(original) {
		t.Fatalf("failed startup modified encrypted store: bytes=%q err=%v", got, readErr)
	}
}

func TestLocalAIIsOptInAndPersistsAcrossAppRestart(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode model request: %v", err)
		}
		if request.Prompt == "" || len(request.Prompt) > 20<<10 {
			t.Errorf("unexpected model prompt length %d", len(request.Prompt))
		}
		if strings.Contains(request.Prompt, "ultrasecret-fixture") || !strings.Contains(request.Prompt, "[REDACTED:SECRET]") {
			t.Error("model request did not contain only sanitized note text")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":"{\"proposals\":[{\"kind\":\"task\",\"text\":\"Send the revised plan\"}]}"}`))
	}))
	defer server.Close()

	dir := filepath.Join(t.TempDir(), "Blur")
	secrets := &bootstrapSecrets{values: make(map[string][]byte)}
	app, err := openLocalAppAt(context.Background(), dir, secrets, rand.Reader)
	if err != nil {
		t.Fatalf("open app: %v", err)
	}
	if app.Client.LocalAIEnabled() {
		t.Fatal("model enabled without explicit user opt-in")
	}
	note := "Meeting with Sam about launch; password: ultrasecret-fixture"
	result, err := app.Client.AddNote(note)
	if err != nil || requests.Load() != 0 || result.Processing == "model_processed" {
		t.Fatalf("disabled model was called: result=%+v requests=%d err=%v", result, requests.Load(), err)
	}
	settings := LocalAISettings{Enabled: true, Endpoint: server.URL, Model: "test-model"}
	if err := app.Client.ConfigureLocalAI(context.Background(), settings); err != nil {
		t.Fatalf("enable local model: %v", err)
	}
	result, err = app.Client.AddNote(note)
	if err != nil || requests.Load() != 1 || result.Processing != "model_processed" {
		t.Fatalf("enabled model flow: result=%+v requests=%d err=%v", result, requests.Load(), err)
	}
	if len(result.Proposals) == 0 || result.Proposals[0].Text != "Send the revised plan" {
		t.Fatalf("model proposal missing from result: %+v", result.Proposals)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = openLocalAppAt(context.Background(), dir, secrets, rand.Reader)
	if err != nil {
		t.Fatalf("reopen app: %v", err)
	}
	defer app.Close()
	if !app.Client.LocalAIEnabled() || app.Client.LocalAISettings() != settings {
		t.Fatal("explicit local model setting did not survive restart")
	}
	if requests.Load() != 1 {
		t.Fatal("reopening app contacted model without a new note")
	}
}

func TestOpenAICompatibleProviderStoresCredentialOnlyInOSSecretStore(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer provider-secret" {
			t.Errorf("provider credential missing from request")
		}
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		body, _ := json.Marshal(request)
		if strings.Contains(string(body), "ultrasecret-provider-test") || !strings.Contains(string(body), "[REDACTED:SECRET]") {
			t.Error("provider received unsanitized quick-note text")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"proposals\":[]}"}}]}`))
	}))
	defer server.Close()
	dir := filepath.Join(t.TempDir(), "Blur")
	secrets := &bootstrapSecrets{values: make(map[string][]byte)}
	app, err := openLocalAppAt(context.Background(), dir, secrets, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	settings := LocalAISettings{Enabled: true, Provider: ProviderOpenAICompatible, Endpoint: server.URL + "/v1", Model: "test-model"}
	if err := app.Client.ConfigureAI(context.Background(), settings, "provider-secret"); err != nil {
		t.Fatalf("configure provider: %v", err)
	}
	configBytes, err := os.ReadFile(filepath.Join(dir, "local-ai.json"))
	if err != nil || strings.Contains(string(configBytes), "provider-secret") {
		t.Fatalf("provider credential was written to preferences: %q err=%v", configBytes, err)
	}
	clear(configBytes)
	var storedCredential string
	for name, value := range secrets.values {
		if strings.HasPrefix(name, openAICompatibleSecretName) {
			storedCredential = string(value)
		}
	}
	if storedCredential != "provider-secret" {
		t.Fatal("provider credential was not stored in the OS secret store")
	}
	note := "Meeting with Sam; password: ultrasecret-provider-test"
	if _, err := app.Client.AddNote(note); err != nil || requests.Load() != 1 {
		t.Fatalf("configured provider call: requests=%d err=%v", requests.Load(), err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = openLocalAppAt(context.Background(), dir, secrets, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if !app.Client.LocalAIEnabled() || requests.Load() != 1 {
		t.Fatal("provider setting was not restored without an unsolicited request")
	}
	if _, err := app.Client.AddNote(note); err != nil || requests.Load() != 2 {
		t.Fatalf("restored provider call: requests=%d err=%v", requests.Load(), err)
	}
	if err := app.Client.ConfigureAI(context.Background(), LocalAISettings{}, ""); err != nil {
		t.Fatalf("disable provider: %v", err)
	}
	if app.Client.LocalAIEnabled() {
		t.Fatal("provider remained enabled after disable")
	}
	for name := range secrets.values {
		if strings.HasPrefix(name, openAICompatibleSecretName) {
			t.Fatal("disabled provider credential remained in OS secret store")
		}
	}
}

func TestBootstrappedTodayBuildsAndPersistsActivityRecap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Blur")
	secrets := &bootstrapSecrets{values: make(map[string][]byte)}
	app, err := openLocalAppAt(context.Background(), dir, secrets, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	current := time.Now().In(time.Local)
	now := time.Date(current.Year(), current.Month(), current.Day(), 12, 0, 0, 0, time.Local)
	segment := activity.Segment{
		ID: "today-recap-test-segment", WallStart: now.Add(-20 * time.Minute), WallEnd: now,
		Duration: int64(20 * time.Minute), AppID: "com.example.editor", SourceIDs: []string{"capture-source"},
	}
	if err := app.store.PutActivitySegment(context.Background(), segment); err != nil {
		t.Fatal(err)
	}
	date := now.In(time.Local).Format("2006-01-02")
	got, err := app.Client.Today(date)
	if err != nil {
		t.Fatalf("build Today recap: %v", err)
	}
	if got.State != "ready" || len(got.Snapshot.ActiveSubjects) != 1 || len(got.Snapshot.TimeSplit) != 1 || got.Snapshot.TimeSplit[0].Duration != 20*time.Minute {
		t.Fatalf("unexpected durable activity recap: %+v", got)
	}
	stored, found, err := app.store.GetTodaySnapshot(context.Background(), date)
	if err != nil || !found || len(stored.ActiveSubjects) != 1 {
		t.Fatalf("Today recap was not persisted: found=%t subjects=%d err=%v", found, len(stored.ActiveSubjects), err)
	}
}

func TestOpenLocalAppNeverReplacesMissingKeyForExistingStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Blur")
	secrets := &bootstrapSecrets{values: make(map[string][]byte)}
	app, err := openLocalAppAt(context.Background(), dir, secrets, rand.Reader)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	delete(secrets.values, "database")
	putsBefore := secrets.puts
	if _, err := openLocalAppAt(context.Background(), dir, secrets, rand.Reader); err == nil {
		t.Fatal("opened existing encrypted store with a replacement key")
	}
	if secrets.puts != putsBefore {
		t.Fatalf("created %d key(s) while opening existing store", secrets.puts-putsBefore)
	}
}

func TestOpenLocalAppDoesNotFollowStoreSymlink(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Blur")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("do not touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "memory.db")); err != nil {
		t.Fatal(err)
	}
	secrets := &bootstrapSecrets{values: make(map[string][]byte)}
	if _, err := openLocalAppAt(context.Background(), dir, secrets, rand.Reader); err == nil {
		t.Fatal("opened a symlinked store")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "do not touch" {
		t.Fatalf("symlink target changed: %q, err=%v", got, err)
	}
}

type bootstrapSecrets struct {
	values map[string][]byte
	puts   int
	getErr error
}

func (*bootstrapSecrets) Capability(context.Context) (platform.CapabilityReport, error) {
	return platform.CapabilityReport{ID: "secret_store", State: platform.Available, Evidence: "test"}, nil
}
func (s *bootstrapSecrets) Get(_ context.Context, name string, max int) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	value, ok := s.values[name]
	if !ok {
		return nil, &platform.SecretStoreError{State: platform.SecretNotFound}
	}
	if len(value) > max {
		return nil, &platform.SecretStoreError{State: platform.SecretCorrupt}
	}
	return append([]byte(nil), value...), nil
}
func (s *bootstrapSecrets) Put(_ context.Context, name string, value []byte) error {
	if _, ok := s.values[name]; ok {
		return errors.New("already exists")
	}
	s.values[name] = append([]byte(nil), value...)
	s.puts++
	return nil
}
func (s *bootstrapSecrets) Delete(_ context.Context, name string) error {
	delete(s.values, name)
	return nil
}

func TestLocalAppCloseThenClientOperationsFailPromptly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Blur")
	app, err := openLocalAppAt(context.Background(), dir, &bootstrapSecrets{values: make(map[string][]byte)}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	client := app.Client
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() { _, err := client.AskMemory(context.Background(), "what happened"); done <- err }()
	go func() { _, err := client.Today("2026-11-05"); done <- err }()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("operation after Close succeeded")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("operation after Close hung")
		}
	}
}
