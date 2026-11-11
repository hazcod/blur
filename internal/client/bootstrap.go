package client

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/power"
	"github.com/hazcod/blur/internal/preferences"
	"github.com/hazcod/blur/internal/retention"
	"github.com/hazcod/blur/internal/securestore"
	"github.com/hazcod/blur/internal/storage/durable"
	"github.com/hazcod/blur/internal/storage/physical/bolt"
)

// bootstrapLockTimeout lets a relaunch wait out a slow shutdown of the prior
// instance (retention sweep, store close) before reporting it as running.
// chronicleRecoveryTimeout bounds the synchronous startup recovery pass.
const chronicleRecoveryTimeout = 10 * time.Second

var bootstrapLockTimeout = 5 * time.Second

const dataKeyName = "database"

var ErrMissingDataKey = errors.New("macos client: encrypted store has no key")
var ErrAlreadyRunning = errors.New("macos client: another instance is already running")
var ErrEncryptedStoreCorrupt = errors.New("macos client: encrypted store is corrupt")

// LocalApp owns the encrypted store and services bound to it. Close it when
// the desktop event loop exits.
type LocalApp struct {
	Client          *Client
	store           *durable.Store
	activity        *activity.Engine
	activityEpoch   time.Time
	localAI         *localAIController
	preferences     *preferences.Store
	mcpControl      MCPPreferenceControl
	chronicleFeed   *chronicleFeed
	retentionCancel context.CancelFunc
	retentionDone   chan struct{}
	replica         *replicaService
	releaseLock     func()
}

func (a *LocalApp) Close() error {
	if a == nil {
		return nil
	}
	// Push pending sync changes while the store is still open.
	a.replica.Close()
	// Cancel and drain in-flight client operations before tearing anything down.
	if a.Client != nil {
		a.Client.Close()
	}
	if a.chronicleFeed != nil {
		a.chronicleFeed.Close()
	}
	var err error
	if a.mcpControl != nil {
		err = errors.Join(err, a.mcpControl.Close())
		a.mcpControl = nil
	}
	if a.localAI != nil {
		a.localAI.Close()
	}
	if a.retentionCancel != nil {
		a.retentionCancel()
		if a.retentionDone != nil {
			<-a.retentionDone
		}
	}
	if a.store != nil {
		err = errors.Join(err, a.store.Close())
		a.store = nil
	}
	if a.releaseLock != nil {
		a.releaseLock()
		a.releaseLock = nil
	}
	return err
}

// OpenLocalApp uses the platform configuration directory and the supplied OS
// secret store. It never falls back to an unencrypted or fixture store.
func OpenLocalApp(ctx context.Context, secrets platform.SecretStore) (*LocalApp, error) {
	if uiSnapshotDir() != "" {
		return openUISnapshotApp(ctx)
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil, ErrNotConfigured
	}
	directory := filepath.Join(configDir, "Blur")
	// A fresh App Sandbox container has no Application Support directory yet.
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return nil, ErrNotConfigured
	}
	// App Sandbox first run: copy (never move) a pre-sandbox store into the container.
	if sandboxed() {
		migrateLegacyStore(directory, legacyDataDirectory())
	}
	return openLocalAppAt(ctx, directory, secrets, rand.Reader)
}

func openLocalAppAt(ctx context.Context, directory string, secrets platform.SecretStore, random io.Reader) (*LocalApp, error) {
	if ctx == nil || ctx.Err() != nil || secrets == nil || random == nil || directory == "" {
		return nil, ErrNotConfigured
	}
	if err := bolt.CreatePrivateDirectory(directory); err != nil {
		return nil, ErrNotConfigured
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, bootstrapLockTimeout)
	releaseLock, lockErr := acquireBootstrapLock(lockCtx, directory)
	cancelLock()
	if lockErr != nil {
		if errors.Is(lockErr, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, ErrAlreadyRunning
		}
		return nil, ErrNotConfigured
	}
	keepLock := false
	defer func() {
		if !keepLock {
			releaseLock()
		}
	}()
	path := filepath.Join(directory, "memory.db")
	probe, err := bolt.OpenExisting(path)
	storeExists := err == nil
	if storeExists {
		if closeErr := probe.Close(); closeErr != nil {
			return nil, ErrNotConfigured
		}
	} else if !errors.Is(err, bolt.ErrNotFound) {
		return nil, classifyStoreOpenError(err)
	}

	var key securestore.Key
	if storeExists {
		key, err = securestore.LoadKey(ctx, secrets, dataKeyName)
		if isSecretState(err, platform.SecretNotFound) {
			return nil, ErrMissingDataKey
		}
	} else {
		key, err = securestore.ResolveKey(ctx, secrets, dataKeyName, random)
	}
	if err != nil {
		return nil, errors.Join(ErrNotConfigured, err)
	}
	mode := durable.CreateNew
	if storeExists {
		mode = durable.OpenExisting
	}
	store, err := durable.OpenDurable(path, key, mode)
	if !storeExists && errors.Is(err, durable.ErrAlreadyExists) {
		store, err = durable.OpenDurable(path, key, durable.OpenExisting)
	}
	if err != nil {
		return nil, classifyStoreOpenError(err)
	}
	activityEngine := activity.New(activity.Config{})
	activityEpoch := time.Now()
	deleteService, err := deletion.NewService(store, activityEngine)
	if err != nil {
		_ = store.Close()
		return nil, ErrNotConfigured
	}
	retentionService, err := retention.NewService(store, deleteService)
	if err != nil {
		_ = store.Close()
		return nil, ErrNotConfigured
	}
	noteService := notes.NewService(store, nil, nil)
	localAI := newLocalAIControllerContext(ctx, filepath.Join(directory, "local-ai.json"), noteService, secrets)
	powerController, err := power.NewController(filepath.Join(directory, "power.json"))
	if err != nil {
		_ = store.Close()
		return nil, ErrNotConfigured
	}
	preferencesStore, err := preferences.NewStore(filepath.Join(directory, "preferences.json"))
	if err != nil {
		_ = store.Close()
		return nil, ErrNotConfigured
	}
	feed := newChronicleFeed()
	client := NewClient(store).WithNotes(noteService).WithDeletion(deleteService).WithLocalAI(localAI).WithRetention(retentionService).WithPower(powerController).WithPreferences(preferencesStore)
	localAI.SetChronicleRuntime(sourceVerifiedRepository{inner: store, sources: store, client: client}, powerController, feed)
	client.addMemoryChangingHook(feed.Purge)
	mcpControl, mcpErr := newMCPPreferenceControl(directory, secrets, client)
	if mcpErr == nil && mcpControl != nil {
		client.WithMCPControl(mcpControl)
	} else {
		slog.Warn("local Claude bridge unavailable", "result", "unsupported")
	}
	replicaSync, replicaErr := newReplicaService(directory, store, deleteService, client, secrets)
	if replicaErr == nil {
		client.replica = replicaSync
	} else {
		slog.Warn("sync across Macs unavailable", "result", "unsupported")
	}
	retentionService.SetBeforeSweep(client.NotifyMemoryChanging)
	retentionService.SetAfterSweep(client.NotifyMemoryStable)
	sweepCtx, cancelSweep := context.WithTimeout(ctx, 5*time.Minute)
	_, _, sweepErr := retentionService.RunIfDue(sweepCtx, time.Now())
	cancelSweep()
	if sweepErr != nil {
		slog.Error("startup retention sweep failed", "result", "failed")
	}
	client.retentionStartupFailed = sweepErr != nil
	retentionCtx, retentionCancel := context.WithCancel(context.Background())
	retentionDone := make(chan struct{})
	go func() {
		defer close(retentionDone)
		retentionService.RunPeriodic(retentionCtx, retention.SweepInterval)
	}()
	app := &LocalApp{Client: client, store: store, activity: activityEngine, activityEpoch: activityEpoch, localAI: localAI, preferences: preferencesStore, mcpControl: mcpControl, replica: replicaSync, chronicleFeed: feed, retentionCancel: retentionCancel, retentionDone: retentionDone, releaseLock: releaseLock}
	recoveryCtx, cancelRecovery := context.WithTimeout(ctx, chronicleRecoveryTimeout)
	recoveryErr := recoverClosedChronicles(recoveryCtx, time.Now(), store, localAI)
	cancelRecovery()
	if recoveryErr != nil {
		slog.Warn("closed chronicle recovery interrupted", "result", "interrupted")
	}
	replicaSync.start() // after the retention sweep and recovery; nil-safe
	keepLock = true
	return app, nil
}

func classifyStoreOpenError(err error) error {
	if errors.Is(err, bolt.ErrCorrupt) || errors.Is(err, durable.ErrCorrupt) {
		return ErrEncryptedStoreCorrupt
	}
	return ErrNotConfigured
}

func (a *LocalApp) notifyChronicleEvidence(observation capture.SanitizedObservation, at time.Time) {
	if a != nil && a.chronicleFeed != nil {
		a.chronicleFeed.Add(observation, at)
	}
}

func isSecretState(err error, state platform.SecretState) bool {
	var secretErr *platform.SecretStoreError
	return errors.As(err, &secretErr) && secretErr != nil && secretErr.State == state
}
