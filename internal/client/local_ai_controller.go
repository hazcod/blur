package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/knowledge/derive"
	"github.com/hazcod/blur/internal/notes"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/power"
)

var errLocalAIUnavailable = errors.New("macos client: local AI configuration unavailable")

const (
	localAIStartupKeychainTimeout = 10 * time.Second
	localAIKeychainCleanupTimeout = 5 * time.Second
)

type localAIController struct {
	// configMu serializes writers (Configure, SetBackgroundSummaries) so that
	// slow Keychain and file I/O can run without holding mu. mu only guards the
	// in-memory state read by UI-thread callers and is held briefly.
	configMu        sync.Mutex
	mu              sync.RWMutex
	path            string
	service         *notes.Service
	secrets         platform.SecretStore
	settings        LocalAISettings
	askModel        configuredAskModel
	chronicleRepo   chronicle.SummaryRepository
	chroniclePolicy chronicle.BackgroundPolicy
	chronicleFeed   *chronicleFeed
	chronicleWorker *chronicle.Worker
	needsRepair     bool
}

func newLocalAIController(path string, service *notes.Service, secretStores ...platform.SecretStore) *localAIController {
	return newLocalAIControllerContext(context.Background(), path, service, secretStores...)
}

// newLocalAIControllerContext bounds the startup Keychain read so a locked
// Keychain cannot hang startup.
func newLocalAIControllerContext(ctx context.Context, path string, service *notes.Service, secretStores ...platform.SecretStore) *localAIController {
	if ctx == nil {
		ctx = context.Background()
	}
	cleanPath := ""
	if path != "" {
		cleanPath = filepath.Clean(path)
	}
	controller := &localAIController{path: cleanPath, service: service}
	if len(secretStores) > 0 {
		controller.secrets = secretStores[0]
	}
	settings, err := readLocalAISettings(path)
	if err != nil {
		controller.needsRepair = true
		return controller
	}
	controller.settings = settings
	if settings.Enabled && service != nil {
		readCtx, cancel := context.WithTimeout(ctx, localAIStartupKeychainTimeout)
		credential, err := controller.readCredential(readCtx, settings.CredentialRef)
		cancel()
		if err != nil {
			controller.needsRepair = true
			return controller
		}
		extractor, err := newConfiguredExtractor(settings, credential)
		if err != nil {
			controller.needsRepair = true
			return controller
		}
		if effectiveProvider(settings) == ProviderOllama {
			controller.askModel, err = newConfiguredAskModel(settings, credential)
		} else if settings.RemoteAskEnabled {
			controller.askModel, err = newConfiguredAskModel(settings, credential)
		}
		if err != nil {
			controller.needsRepair = true
			return controller
		}
		service.SetExtractor(extractor)
	}
	return controller
}

func (c *localAIController) Configure(ctx context.Context, settings LocalAISettings, credentials ...string) error {
	if c == nil || ctx == nil || ctx.Err() != nil || c.path == "" || c.service == nil || validateLocalAISettings(settings) != nil {
		return errLocalAIUnavailable
	}
	if len(credentials) > 1 {
		return errLocalAIUnavailable
	}
	providedCredential := len(credentials) == 1
	credential := ""
	if providedCredential {
		credential = credentials[0]
	}
	c.configMu.Lock()
	defer c.configMu.Unlock()
	previous := c.Settings()
	previousRef := previous.CredentialRef
	provider := settings.Provider
	if provider == "" && (settings.Enabled || settings.Endpoint != "" || settings.Model != "") {
		provider = ProviderOllama
	}
	sameRemoteAskTarget := previous.Enabled && effectiveProvider(previous) == ProviderOpenAICompatible && provider == ProviderOpenAICompatible && previous.Endpoint == settings.Endpoint && previous.Model == settings.Model
	if settings.RemoteAskEnabled && !sameRemoteAskTarget {
		if previous.RemoteAskEnabled {
			// Consent never follows a changed destination, even for callers that
			// bypass the desktop's draft-state clearing.
			settings.RemoteAskEnabled = false
		} else {
			// First-time consent is a separate action after the provider and
			// destination have already been saved.
			return errLocalAIUnavailable
		}
	}
	settings.CredentialRef = ""
	newSecret := false
	if provider == ProviderOpenAICompatible && settings.Enabled {
		if providedCredential && credential != "" {
			ref, err := newCredentialRef()
			if err != nil {
				return errLocalAIUnavailable
			}
			settings.CredentialRef = ref
			newSecret = true
		} else if previousRef != "" && effectiveProvider(previous) == ProviderOpenAICompatible {
			settings.CredentialRef = previousRef
			loaded, err := c.readCredential(ctx, previousRef)
			if err != nil {
				return errLocalAIUnavailable
			}
			credential = loaded
			defer clear([]byte(loaded))
		}
	}
	var extractor notes.Extractor
	if settings.Enabled {
		var err error
		extractor, err = newConfiguredExtractor(settings, credential)
		if err != nil {
			return errLocalAIUnavailable
		}
	}
	var askModel configuredAskModel
	if settings.Enabled && (effectiveProvider(settings) == ProviderOllama || settings.RemoteAskEnabled) {
		var err error
		askModel, err = newConfiguredAskModel(settings, credential)
		if err != nil {
			return errLocalAIUnavailable
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if newSecret {
		credentialBytes := []byte(credential)
		putErr := error(nil)
		if c.secrets == nil {
			putErr = errLocalAIUnavailable
		} else {
			putErr = c.secrets.Put(ctx, providerSecretName(settings.CredentialRef), credentialBytes)
		}
		clear(credentialBytes)
		if putErr != nil {
			return errLocalAIUnavailable
		}
	}
	if err := writeLocalAISettings(c.path, settings); err != nil {
		if newSecret && c.secrets != nil {
			cleanupCtx, cancel := context.WithTimeout(ctx, localAIKeychainCleanupTimeout)
			_ = c.secrets.Delete(cleanupCtx, providerSecretName(settings.CredentialRef))
			cancel()
		}
		return errLocalAIUnavailable
	}
	c.mu.Lock()
	c.service.SetExtractor(extractor)
	previousModel := c.askModel
	c.askModel = askModel
	c.settings = settings
	c.needsRepair = false
	previousWorker := c.replaceChronicleWorkerLocked()
	c.mu.Unlock()
	if previousWorker != nil {
		previousWorker.Close()
	}
	if previousModel != nil {
		previousModel.CloseIdleConnections()
	}
	if previousRef != "" && previousRef != settings.CredentialRef {
		// The new settings are already persisted and applied; a leftover
		// superseded secret is a cleanup problem, not a failed save.
		deleteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), localAIKeychainCleanupTimeout)
		if c.secrets == nil || c.secrets.Delete(deleteCtx, providerSecretName(previousRef)) != nil {
			slog.Warn("superseded local AI credential could not be removed", "result", "delete_failed")
		}
		cancel()
	}
	return nil
}

func (c *localAIController) SetChronicleRuntime(repository chronicle.SummaryRepository, policy chronicle.BackgroundPolicy, feed *chronicleFeed) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.chronicleRepo, c.chroniclePolicy, c.chronicleFeed = repository, policy, feed
	previous := c.replaceChronicleWorkerLocked()
	c.mu.Unlock()
	if previous != nil {
		previous.Close()
	}
}

func (c *localAIController) SetBackgroundSummaries(ctx context.Context, enabled bool) error {
	if c == nil || ctx == nil || ctx.Err() != nil {
		return errLocalAIUnavailable
	}
	c.configMu.Lock()
	defer c.configMu.Unlock()
	c.mu.RLock()
	settings, needsRepair := c.settings, c.needsRepair
	c.mu.RUnlock()
	settings.BackgroundSummaries = enabled
	if needsRepair || enabled && (!settings.Enabled || effectiveProvider(settings) != ProviderOllama) || validateLocalAISettings(settings) != nil {
		return errLocalAIUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writeLocalAISettings(c.path, settings); err != nil {
		return errLocalAIUnavailable
	}
	c.mu.Lock()
	c.settings = settings
	previous := c.replaceChronicleWorkerLocked()
	c.mu.Unlock()
	if previous != nil {
		previous.Close()
	}
	return nil
}

// replaceChronicleWorkerLocked installs the replacement worker and returns the
// previous one; the caller must Close it after releasing c.mu.
func (c *localAIController) replaceChronicleWorkerLocked() *chronicle.Worker {
	previous := c.chronicleWorker
	c.chronicleWorker = nil
	settings := c.settings
	if settings.Enabled && settings.BackgroundSummaries && !c.needsRepair && effectiveProvider(settings) == ProviderOllama && c.chronicleRepo != nil && c.chroniclePolicy != nil {
		generator, err := chronicle.NewOllamaGenerator(settings.Endpoint, settings.Model)
		if err == nil {
			c.chronicleWorker = chronicle.NewWorker(c.chronicleRepo, c.chroniclePolicy, generator, chronicle.WorkerConfig{Enabled: true, Provider: power.LocalProvider})
		}
	}
	if c.chronicleFeed != nil {
		c.chronicleFeed.SetWorker(c.chronicleWorker)
	}
	return previous
}

type configuredAskModel interface {
	assistant.Generator
	assistant.TokenCounter
	CloseIdleConnections()
}

func newConfiguredAskModel(settings LocalAISettings, credential string) (configuredAskModel, error) {
	switch effectiveProvider(settings) {
	case ProviderOllama:
		return assistant.NewOllamaGenerator(settings.Endpoint, settings.Model)
	case ProviderOpenAICompatible:
		return assistant.NewOpenAICompatibleGenerator(settings.Endpoint, settings.Model, credential)
	default:
		return nil, errLocalAIUnavailable
	}
}

func (c *localAIController) askService(retriever assistant.Retriever) (*assistant.Service, bool) {
	if c == nil || retriever == nil {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	provider := effectiveProvider(c.settings)
	if !c.settings.Enabled || c.needsRepair || c.service == nil || provider != ProviderOllama && !(provider == ProviderOpenAICompatible && c.settings.RemoteAskEnabled) || c.askModel == nil {
		return nil, false
	}
	return assistant.NewService(retriever, c.askModel, c.askModel, assistant.ContextTokenLimit), true
}

func (c *localAIController) knowledgeGenerator() (derive.Generator, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	settings, available := c.settings, c.settings.Enabled && !c.needsRepair && c.service != nil && c.service.AIConfigured() && effectiveProvider(c.settings) == ProviderOllama
	c.mu.RUnlock()
	if !available {
		return nil, false
	}
	generator, err := derive.NewOllamaGenerator(settings.Endpoint, settings.Model)
	if err != nil {
		return nil, false
	}
	return generator, true
}

func (c *localAIController) canSuggestKnowledge() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.settings.Enabled && !c.needsRepair && c.service != nil && c.service.AIConfigured() && effectiveProvider(c.settings) == ProviderOllama
}

func effectiveProvider(settings LocalAISettings) string {
	if settings.Provider != "" {
		return settings.Provider
	}
	return ProviderOllama
}

func newConfiguredExtractor(settings LocalAISettings, credential string) (notes.Extractor, error) {
	switch effectiveProvider(settings) {
	case ProviderOllama:
		return notes.NewOllamaExtractor(settings.Endpoint, settings.Model)
	case ProviderOpenAICompatible:
		return notes.NewOpenAICompatibleExtractor(settings.Endpoint, settings.Model, credential)
	default:
		return nil, errLocalAIUnavailable
	}
}

func (c *localAIController) readCredential(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	if c.secrets == nil || !validCredentialRef(ref) {
		return "", errLocalAIUnavailable
	}
	value, err := c.secrets.Get(ctx, providerSecretName(ref), 4096)
	if err != nil || len(value) == 0 || len(value) > 4096 {
		clear(value)
		return "", errLocalAIUnavailable
	}
	credential := string(value)
	clear(value)
	return credential, nil
}

func providerSecretName(ref string) string { return openAICompatibleSecretName + ref }

func newCredentialRef() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		clear(value[:])
		return "", err
	}
	ref := hex.EncodeToString(value[:])
	clear(value[:])
	return ref, nil
}

func (c *localAIController) Settings() LocalAISettings {
	if c == nil {
		return LocalAISettings{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.settings
}

func (c *localAIController) Enabled() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.settings.Enabled && c.service != nil && c.service.AIConfigured()
}

func (c *localAIController) NeedsRepair() bool {
	if c == nil {
		return true
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.needsRepair
}

func (c *localAIController) Close() {
	if c != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.service != nil {
			c.service.SetExtractor(nil)
		}
		if c.askModel != nil {
			c.askModel.CloseIdleConnections()
			c.askModel = nil
		}
		if c.chronicleWorker != nil {
			c.chronicleWorker.Close()
			c.chronicleWorker = nil
		}
	}
}

func (c *localAIController) ScheduleClosedHour(ctx context.Context, hour chronicle.Hour) error {
	if c == nil {
		return errLocalAIUnavailable
	}
	c.mu.RLock()
	worker := c.chronicleWorker
	c.mu.RUnlock()
	if worker == nil {
		return chronicle.ErrWorkerDisabled
	}
	return worker.Schedule(ctx, hour)
}

func (c *localAIController) BackgroundSummariesEnabled() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.chronicleWorker != nil
}
