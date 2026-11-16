package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/chatgpt"
	"github.com/hazcod/blur/internal/model"
)

type cloudLoginFlow interface {
	AuthorizationURL() string
	Wait(context.Context) (chatgpt.Account, error)
	Cancel()
}
type cloudAccountManager interface {
	Accounts(context.Context) ([]chatgpt.Account, error)
	BeginSignIn(context.Context, string) (cloudLoginFlow, error)
	ListModels(context.Context, string) ([]chatgpt.Model, error)
	Disconnect(context.Context, string) (bool, error)
}
type managedCloudAccounts struct{ manager *chatgpt.Manager }

func (m managedCloudAccounts) Accounts(ctx context.Context) ([]chatgpt.Account, error) {
	return m.manager.Accounts(ctx)
}
func (m managedCloudAccounts) BeginSignIn(ctx context.Context, id string) (cloudLoginFlow, error) {
	return m.manager.BeginSignIn(ctx, id)
}
func (m managedCloudAccounts) ListModels(ctx context.Context, id string) ([]chatgpt.Model, error) {
	return m.manager.ListModels(ctx, id)
}
func (m managedCloudAccounts) Disconnect(ctx context.Context, id string) (bool, error) {
	return m.manager.Disconnect(ctx, id)
}

type cloudModelChoice struct{ ID, Label string }
type cloudCatalogKey struct{ provider, account, workspace, credential string }
type cloudAIState struct {
	accounts            []chatgpt.Account
	models              []cloudModelChoice
	catalog             cloudCatalogKey
	busy, login, loaded bool
	request             uint64
	cancel              context.CancelFunc
	status              string
	accountsManager     cloudAccountManager
	openAuthorization   func(string) error
}

func (d *desktop) cloudManager() cloudAccountManager {
	if d.cloudAI.accountsManager != nil {
		return d.cloudAI.accountsManager
	}
	if d.client != nil && d.client.localAI != nil && d.client.localAI.chatGPT != nil {
		return managedCloudAccounts{manager: d.client.localAI.chatGPT}
	}
	return nil
}
func (d *desktop) cloudUpdate() {
	if d.app != nil {
		d.app.RequestUpdate()
	}
}
func (d *desktop) cancelCloudOperation() {
	if d == nil {
		return
	}
	d.cloudAI.request++
	if d.cloudAI.cancel != nil {
		d.cloudAI.cancel()
		d.cloudAI.cancel = nil
	}
	d.cloudAI.busy, d.cloudAI.login = false, false
}
func (d *desktop) cancelCloudSignIn() {
	d.cancelCloudOperation()
	d.cloudAI.status = "Sign-in cancelled. Your selected model is unchanged."
	d.cloudUpdate()
}
func (d *desktop) cloudCatalogIdentity() cloudCatalogKey {
	credential := ""
	if d.modelCredential != "" {
		sum := sha256.Sum256([]byte(d.modelCredential))
		credential = hex.EncodeToString(sum[:])
	} else if d.client != nil {
		saved := d.client.LocalAISettings()
		if effectiveProvider(saved) == d.modelProvider {
			credential = saved.CredentialRef
		}
	}
	return cloudCatalogKey{provider: d.modelProvider, account: d.modelAccountID, workspace: strings.TrimSpace(d.modelWorkspaceID), credential: credential}
}
func (d *desktop) selectCloudAccount(id string) bool {
	for _, a := range d.cloudAI.accounts {
		if a.ID != id || !a.Connected || !a.InferenceEnabled {
			continue
		}
		if d.modelAccountID != id {
			d.cancelCloudOperation()
			d.modelAccountID = id
			d.modelName = ""
			d.remoteAskEnabled, d.remoteNotesEnabled = false, false
			d.cloudAI.models = nil
		}
		return true
	}
	return false
}
func (d *desktop) selectCloudModel(id string) bool {
	if d.cloudAI.catalog != d.cloudCatalogIdentity() {
		return false
	}
	for _, item := range d.cloudAI.models {
		if item.ID == id {
			if d.modelName != id {
				d.modelName = id
				d.remoteAskEnabled, d.remoteNotesEnabled = false, false
			}
			return true
		}
	}
	return false
}
func (d *desktop) cloudDraftMatchesSaved(saved LocalAISettings) bool {
	return saved.Enabled && saved.Provider == d.modelProvider && saved.Model == strings.TrimSpace(d.modelName) && saved.AccountID == d.modelAccountID && saved.WorkspaceID == strings.TrimSpace(d.modelWorkspaceID) && d.modelCredential == "" && cloudAccountProvider(d.modelProvider)
}
func (d *desktop) cloudSettingsDraft(saved LocalAISettings, enabled bool) LocalAISettings {
	saved.Enabled = enabled
	saved.Provider = d.modelProvider
	saved.Endpoint = ""
	saved.Model = strings.TrimSpace(d.modelName)
	saved.CredentialRef = ""
	saved.AccountID, saved.WorkspaceID = "", ""
	saved.BackgroundSummaries, saved.AutomaticLibrary = false, false
	saved.RemoteAskEnabled = enabled && d.remoteAskEnabled
	saved.RemoteNotesEnabled = enabled && d.remoteNotesEnabled
	switch d.modelProvider {
	case ProviderChatGPT:
		saved.AccountID = d.modelAccountID
	case ProviderClaude:
		saved.WorkspaceID = strings.TrimSpace(d.modelWorkspaceID)
	}
	return saved
}
func (d *desktop) cloudDraftReady(saved LocalAISettings) bool {
	if d.loading || d.cloudAI.busy || strings.TrimSpace(d.modelName) == "" {
		return false
	}
	if d.cloudDraftMatchesSaved(saved) {
		return true
	}
	if d.cloudAI.catalog != d.cloudCatalogIdentity() {
		return false
	}
	found := false
	for _, m := range d.cloudAI.models {
		if m.ID == d.modelName {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	if d.modelProvider == ProviderChatGPT {
		for _, a := range d.cloudAI.accounts {
			if a.ID == d.modelAccountID {
				return a.Connected && a.InferenceEnabled
			}
		}
		return false
	}
	return d.modelProvider == ProviderClaude && (d.modelCredential != "" || saved.Provider == ProviderClaude && saved.CredentialRef != "")
}
func (d *desktop) refreshCloudAccounts() {
	manager := d.cloudManager()
	if manager == nil || d.app == nil || d.cloudAI.busy {
		return
	}
	d.cancelCloudOperation()
	request := d.cloudAI.request
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	d.cloudAI.cancel, d.cloudAI.busy = cancel, true
	d.cloudAI.status = "Reading saved ChatGPT accounts…"
	d.cloudUpdate()
	go func() {
		accounts, err := manager.Accounts(ctx)
		cancel()
		d.app.Post(func() {
			if request != d.cloudAI.request {
				return
			}
			d.cloudAI.busy, d.cloudAI.loaded = false, true
			d.cloudAI.cancel = nil
			if err != nil {
				d.cloudAI.status = "Could not open saved ChatGPT accounts. Check Keychain access and try again."
			} else {
				d.cloudAI.accounts = accounts
				d.cloudAI.status = "Choose a connected account and discover its models."
			}
			d.cloudUpdate()
		})
	}()
}
func (d *desktop) beginChatGPTSignIn(savedID string) {
	manager := d.cloudManager()
	if manager == nil || d.app == nil || d.cloudAI.busy {
		return
	}
	d.cancelCloudOperation()
	request := d.cloudAI.request
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	d.cloudAI.cancel, d.cloudAI.busy, d.cloudAI.login = cancel, true, true
	d.cloudAI.status = "Preparing ChatGPT sign-in…"
	d.cloudUpdate()
	go func() {
		flow, err := manager.BeginSignIn(ctx, savedID)
		d.app.Post(func() {
			if request != d.cloudAI.request {
				if flow != nil {
					flow.Cancel()
				}
				cancel()
				return
			}
			if err != nil {
				cancel()
				d.cloudAI.busy, d.cloudAI.login = false, false
				d.cloudAI.status = cloudAccountErrorCopy(err)
				d.cloudUpdate()
				return
			}
			opener := d.cloudAI.openAuthorization
			if opener == nil {
				opener = openChatGPTAuthorization
			}
			if err := opener(flow.AuthorizationURL()); err != nil {
				flow.Cancel()
				cancel()
				d.cloudAI.busy, d.cloudAI.login = false, false
				d.cloudAI.status = "Could not open the system browser for ChatGPT sign-in. Try again."
				d.cloudUpdate()
				return
			}
			d.cloudAI.status = "Finish Continue with ChatGPT in your browser. This attempt expires after five minutes."
			d.cloudUpdate()
			go func() {
				_, err := flow.Wait(ctx)
				flow.Cancel()
				var accounts []chatgpt.Account
				if err == nil {
					accounts, err = manager.Accounts(ctx)
				}
				cancel()
				d.app.Post(func() {
					if request != d.cloudAI.request {
						return
					}
					d.cloudAI.busy, d.cloudAI.login = false, false
					d.cloudAI.cancel = nil
					if err != nil {
						d.cloudAI.status = cloudAccountErrorCopy(err)
					} else {
						d.cloudAI.accounts, d.cloudAI.loaded = accounts, true
						d.cloudAI.status = "ChatGPT account connected. Choose its model, then select Use ChatGPT plan."
					}
					d.cloudUpdate()
				})
			}()
		})
	}()
}
func cloudAccountErrorCopy(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, chatgpt.ErrCancelled):
		return "ChatGPT sign-in cancelled. Your model selection is unchanged."
	case errors.Is(err, context.DeadlineExceeded):
		return "ChatGPT sign-in timed out. Continue with ChatGPT to try again."
	case errors.Is(err, chatgpt.ErrIdentity):
		return "ChatGPT identity could not be verified. Sign in again; existing accounts are unchanged."
	case errors.Is(err, chatgpt.ErrPermission):
		return "This ChatGPT account has not granted plan usage. Continue with ChatGPT to review permissions."
	case errors.Is(err, chatgpt.ErrStorage):
		return "ChatGPT credentials could not be saved securely. Check Keychain access and the saved account limit."
	default:
		return "Could not connect ChatGPT. Check your connection and try again."
	}
}
func (d *desktop) refreshCloudModels() {
	if d.app == nil || d.cloudAI.busy {
		return
	}
	provider := d.modelProvider
	manager := d.cloudManager()
	if provider == ProviderChatGPT && (manager == nil || d.modelAccountID == "") {
		return
	}
	if provider != ProviderChatGPT && provider != ProviderClaude {
		return
	}
	key := d.cloudCatalogIdentity()
	credential := d.modelCredential
	saved := LocalAISettings{}
	if d.client != nil {
		saved = d.client.LocalAISettings()
	}
	d.cancelCloudOperation()
	request := d.cloudAI.request
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	d.cloudAI.cancel, d.cloudAI.busy = cancel, true
	d.cloudAI.status = "Reading available models…"
	d.cloudUpdate()
	go func() {
		var choices []cloudModelChoice
		var err error
		if provider == ProviderChatGPT {
			var models []chatgpt.Model
			models, err = manager.ListModels(ctx, key.account)
			for _, m := range models {
				choices = append(choices, cloudModelChoice{ID: m.Slug, Label: m.DisplayName})
			}
		} else {
			if credential == "" && saved.Provider == ProviderClaude && saved.CredentialRef != "" && d.client != nil && d.client.localAI != nil {
				credential, err = d.client.localAI.readCredential(ctx, saved.CredentialRef)
			}
			if err == nil {
				var models []model.ClaudeModel
				models, err = model.DiscoverClaudeModels(ctx, credential, key.workspace)
				for _, m := range models {
					choices = append(choices, cloudModelChoice{ID: m.ID, Label: m.Name})
				}
			}
		}
		credential = ""
		cancel()
		d.app.Post(func() {
			if request != d.cloudAI.request || key != d.cloudCatalogIdentity() {
				return
			}
			d.cloudAI.busy = false
			d.cloudAI.cancel = nil
			if err != nil {
				d.cloudAI.models = nil
				d.cloudAI.status = "Could not read available models. Check the selected account, API key, and workspace, then retry."
			} else {
				d.cloudAI.models, d.cloudAI.catalog = choices, key
				d.cloudAI.status = "Choose a model. Connecting and discovery do not select it or enable data permissions."
				if len(choices) == 0 {
					d.cloudAI.status = "No available models were returned for this account."
				}
			}
			d.cloudUpdate()
		})
	}()
}
func (d *desktop) disconnectChatGPTAccount(id string) {
	manager := d.cloudManager()
	if manager == nil || d.app == nil || d.cloudAI.busy {
		return
	}
	d.cancelCloudOperation()
	d.invalidateAsk()
	request := d.cloudAI.request
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	d.cloudAI.cancel, d.cloudAI.busy = cancel, true
	d.cloudAI.status = "Disconnecting ChatGPT account…"
	d.cloudUpdate()
	go func() {
		var disableErr error
		disabledModel := false
		if d.client != nil {
			saved := d.client.LocalAISettings()
			if saved.Enabled && saved.Provider == ProviderChatGPT && saved.AccountID == id {
				saved.Enabled, saved.RemoteAskEnabled, saved.RemoteNotesEnabled = false, false, false
				disableErr = d.client.ConfigureAI(ctx, saved, "")
				disabledModel = disableErr == nil
			}
		}
		confirmed, locallyDisconnected := false, false
		var disconnectErr, loadErr error
		var accounts []chatgpt.Account
		if disableErr == nil {
			confirmed, disconnectErr = manager.Disconnect(ctx, id)
			accounts, loadErr = manager.Accounts(ctx)
			locallyDisconnected = disconnectErr == nil
			if loadErr == nil {
				for _, account := range accounts {
					if account.ID == id {
						locallyDisconnected = !account.Connected
						break
					}
				}
			}
		}
		cancel()
		d.app.Post(func() {
			if request != d.cloudAI.request {
				return
			}
			d.cloudAI.busy = false
			d.cloudAI.cancel = nil
			if disableErr == nil && loadErr == nil {
				d.cloudAI.accounts = accounts
			}
			if d.modelAccountID == id && (disabledModel || locallyDisconnected) {
				d.remoteAskEnabled, d.remoteNotesEnabled = false, false
				d.cloudAI.models = nil
			}
			if d.client != nil && disableErr == nil {
				d.localAIEnabled = d.client.LocalAIEnabled()
			}
			switch {
			case disableErr != nil:
				d.cloudAI.status = "Could not disconnect: the active model could not be disabled. The account and saved permissions are unchanged. Try again."
			case !locallyDisconnected:
				d.cloudAI.status = "Could not confirm local sign-out. The account may remain connected; check Keychain access and retry."
			case !confirmed:
				d.cloudAI.status = "Signed out locally. Server revocation was not confirmed; review connected apps in ChatGPT settings."
			default:
				d.cloudAI.status = "ChatGPT account disconnected. Its registration is retained for signing in again."
			}
			d.cloudUpdate()
		})
	}()
}

func (d *desktop) cloudAISettingsView(saved LocalAISettings, rows []ui.View) ui.View {
	busy := d.loading || d.cloudAI.busy
	if saved.Enabled && cloudAccountProvider(saved.Provider) {
		rows = append(rows, wrapLabel(d.cloudSavedIdentity(saved)).Style(styleMeta))
	}
	if d.modelProvider == ProviderChatGPT {
		rows = append(rows, wrapLabel("Connect your ChatGPT plan. This does not select a model or give Blur access to your ChatGPT conversations or memories.").Style(modern.MutedText), primaryButton("Continue with ChatGPT").Enabled(!busy && d.cloudManager() != nil).OnClick(func() { d.beginChatGPTSignIn("") }), flatButton("Refresh saved accounts").Enabled(!busy && d.cloudManager() != nil).OnClick(d.refreshCloudAccounts))
		for _, a := range d.cloudAI.accounts {
			account := a
			label := account.Label
			if label == "" {
				label = "ChatGPT account"
			}
			suffix := account.ClientID
			if len(suffix) > 8 {
				suffix = suffix[len(suffix)-8:]
			}
			label += " · " + suffix
			if account.Connected && account.InferenceEnabled {
				rows = append(rows, choiceButton(label, d.modelAccountID == account.ID).Enabled(!busy).OnClick(func() {
					if d.selectCloudAccount(account.ID) {
						d.refreshCloudModels()
						d.cloudUpdate()
					}
				}))
			} else {
				status := "Signed out"
				if account.Connected {
					status = "Plan usage not authorized"
				}
				rows = append(rows, wrapLabel(label+" — "+status).Style(modern.MutedText), flatButton("Continue with saved ChatGPT account").Enabled(!busy).OnClick(func() { d.beginChatGPTSignIn(account.ID) }))
			}
		}
		if d.modelAccountID != "" {
			rows = append(rows, flatButton("Discover ChatGPT models").Enabled(!busy).OnClick(d.refreshCloudModels), flatButton("Disconnect selected account").Enabled(!busy).OnClick(func() { d.disconnectChatGPTAccount(d.modelAccountID) }))
		}
		rows = append(rows, wrapLabel("ChatGPT plan usage applies to questions and any separately permitted memory or notes. This preview limits accepted text on this device; it cannot set a hard provider output-token or billing cap.").Style(modern.MutedText), flatButton("Manage usage").OnClick(func() {
			if err := openChatGPTUsage(); err != nil {
				d.cloudAI.status = "Could not open ChatGPT usage settings in the system browser."
				d.cloudUpdate()
			}
		}))
	} else {
		rows = append(rows, wrapLabel("Claude API connection. Claude.ai subscription sign-in is not available for third-party model backends. API usage is billed separately to the key’s workspace.").Style(modern.MutedText), wrapLabel("Claude API key").Style(styleMeta), ui.TextInput().Text(d.modelCredential).Placeholder("API key · stored in OS Keychain when you use this model").Obscured(true).OnText(func(value string) {
			if value != d.modelCredential {
				d.cancelCloudOperation()
				d.remoteAskEnabled, d.remoteNotesEnabled = false, false
				d.cloudAI.models = nil
			}
			d.modelCredential = value
			d.cloudUpdate()
		}), wrapLabel("Workspace ID (optional)").Style(styleMeta), ui.TextInput().Text(d.modelWorkspaceID).Placeholder("Uses the API key’s workspace by default").OnText(func(value string) {
			if value != d.modelWorkspaceID {
				d.cancelCloudOperation()
				d.remoteAskEnabled, d.remoteNotesEnabled = false, false
				d.modelName = ""
				d.cloudAI.models = nil
			}
			d.modelWorkspaceID = value
			d.cloudUpdate()
		}), flatButton("Discover Claude models").Enabled(!busy).OnClick(d.refreshCloudModels))
	}
	if d.cloudAI.status != "" {
		rows = append(rows, wrapLabel(d.cloudAI.status).Style(modern.MutedText))
	}
	if d.cloudAI.login {
		rows = append(rows, flatButton("Cancel sign-in").OnClick(d.cancelCloudSignIn))
	}
	if d.cloudAI.catalog == d.cloudCatalogIdentity() {
		for _, item := range d.cloudAI.models {
			choice := item
			rows = append(rows, choiceButton(choice.Label, d.modelName == choice.ID).Enabled(!busy).OnClick(func() {
				if d.selectCloudModel(choice.ID) {
					d.cloudUpdate()
				}
			}))
		}
	}
	if d.modelName != "" {
		rows = append(rows, wrapLabel("Selected model: "+d.modelName).Style(styleMeta))
	}
	label, ready := d.localAIModelSaveState(saved)
	if !saved.Enabled || !d.cloudDraftMatchesSaved(saved) {
		label = "Use Claude API model"
		if d.modelProvider == ProviderChatGPT {
			label = "Use ChatGPT plan"
		}
	}
	rows = append(rows, primaryButton(label).Enabled(ready).OnClick(func() { d.configureLocalAI(true) }))
	targetSaved := d.cloudDraftMatchesSaved(saved) && !busy
	permissionCopy := "Save this model selection before changing data permissions."
	if targetSaved {
		permissionCopy = "Questions use the selected cloud provider. Saved memory and notes stay on this device unless you enable the permissions below for this exact account and model."
	}
	rows = append(rows, wrapLabel(permissionCopy).Style(modern.MutedText))
	askLabel := "Allow Ask to send matching saved memory"
	if targetSaved && saved.RemoteAskEnabled {
		askLabel = "Stop sending saved memory for Ask"
	}
	notesLabel := "Allow cloud processing of notes I save"
	if targetSaved && saved.RemoteNotesEnabled {
		notesLabel = "Stop cloud note processing"
	}
	rows = append(rows, flatButton(askLabel).Enabled(targetSaved).OnClick(func() { d.remoteAskEnabled = !saved.RemoteAskEnabled; d.configureLocalAI(true) }), flatButton(notesLabel).Enabled(targetSaved).OnClick(func() { d.remoteNotesEnabled = !saved.RemoteNotesEnabled; d.configureLocalAI(true) }))
	if saved.Enabled {
		rows = append(rows, flatButton("Disable answer model").Enabled(!busy).OnClick(func() { d.configureLocalAI(false) }))
	}
	return surfaceBox(14, 8, hugButtons(rows)...)
}

func cloudRequestErrorCopy(err error) string {
	var apiError *chatgpt.APIError
	if errors.As(err, &apiError) {
		switch apiError.Code {
		case "subscription_sharing_usage_limit_exceeded":
			return "ChatGPT plan usage limit reached. Open Manage usage in AI settings; Blur has not switched providers."
		case "subscription_sharing_user_not_eligible":
			return "This ChatGPT account or workspace is not eligible for plan usage. Choose another authorized account in AI settings."
		case "subscription_sharing_usage_unavailable", "subscription_sharing_user_unavailable":
			return "ChatGPT plan usage is temporarily unavailable. Try again later; your account remains connected."
		}
	}
	if errors.Is(err, chatgpt.ErrSignedOut) || errors.Is(err, chatgpt.ErrPermission) {
		return "The selected ChatGPT account needs sign-in or plan permission. Review Continue with ChatGPT in AI settings."
	}
	return ""
}
func cloudProcessingDisclosure(settings LocalAISettings) string {
	name := "the selected Claude API workspace"
	if settings.Provider == ProviderChatGPT {
		name = "the selected ChatGPT account and plan"
	}
	if settings.RemoteAskEnabled {
		return "Searching or asking may send your question and bounded saved passages to " + name + " for recall. Answers use up to eight matching sources. Provider policy and usage charges or plan limits apply."
	}
	return "General questions can use " + name + ". Matching saved memory stays on this device until you separately enable sending it for Ask. Provider policy and usage charges or plan limits apply."
}
func cloudDataFlowCopy(provider string, ask, notes bool) string {
	name := "Claude API"
	if provider == ProviderChatGPT {
		name = "ChatGPT plan"
	}
	copy := "Questions use the selected " + name + " model. "
	if ask {
		copy += "Bounded saved passages can be sent when you search or ask. "
	} else {
		copy += "Saved memory is not sent for search or Ask. "
	}
	if notes {
		copy += "Sanitized notes you save are sent for suggestions. "
	} else {
		copy += "Remote note processing is off. "
	}
	return copy + "Exact search, hourly recaps, and Automatic Library remain local."
}
func (d *desktop) cloudSavedIdentity(saved LocalAISettings) string {
	if saved.Provider == ProviderChatGPT {
		label := "saved account"
		for _, a := range d.cloudAI.accounts {
			if a.ID == saved.AccountID {
				label = a.Label
				break
			}
		}
		if label == "saved account" && len(saved.AccountID) >= 8 {
			label += " · " + saved.AccountID[:8]
		}
		return "Using ChatGPT plan: " + label + ". Model: " + saved.Model + "."
	}
	workspace := saved.WorkspaceID
	if workspace == "" {
		workspace = "API key’s default workspace"
	}
	return "Using Claude API: " + workspace + ". Model: " + saved.Model + "."
}
