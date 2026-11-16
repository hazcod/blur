package client

import (
	"context"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/chatgpt"
)

func TestCloudAccountAndModelChoicesChangeOnlyDraftAndClearConsent(t *testing.T) {
	first, second := strings.Repeat("a", 64), strings.Repeat("b", 64)
	d := &desktop{modelProvider: ProviderChatGPT, modelAccountID: first, modelName: "saved-model", remoteAskEnabled: true, remoteNotesEnabled: true}
	d.cloudAI.accounts = []chatgpt.Account{{ID: first, Connected: true, InferenceEnabled: true}, {ID: second, Connected: true, InferenceEnabled: true}, {ID: strings.Repeat("c", 64), Connected: true}}
	if d.selectCloudAccount(strings.Repeat("c", 64)) {
		t.Fatal("identity-only login selected for inference")
	}
	if !d.selectCloudAccount(second) || d.modelAccountID != second || d.modelName != "" || d.remoteAskEnabled || d.remoteNotesEnabled {
		t.Fatal("account change inherited a model or consent")
	}
	d.cloudAI.models = []cloudModelChoice{{ID: "new-model", Label: "New model"}}
	d.cloudAI.catalog = d.cloudCatalogIdentity()
	if !d.selectCloudModel("new-model") || d.modelName != "new-model" || d.remoteAskEnabled || d.remoteNotesEnabled {
		t.Fatal("model selection enabled remote data processing")
	}
	d.modelAccountID = first
	if d.selectCloudModel("new-model") {
		t.Fatal("stale account catalog changed the draft")
	}
}
func TestCloudDraftAndPermissionsBindProviderAccountWorkspaceAndModel(t *testing.T) {
	id := strings.Repeat("a", 64)
	saved := LocalAISettings{Enabled: true, Provider: ProviderChatGPT, AccountID: id, Model: "chosen", RemoteAskEnabled: true, RemoteNotesEnabled: true}
	d := &desktop{modelProvider: ProviderChatGPT, modelAccountID: id, modelName: "chosen", remoteAskEnabled: true, remoteNotesEnabled: true}
	if !d.cloudDraftMatchesSaved(saved) {
		t.Fatal("saved target mismatched")
	}
	d.modelAccountID = strings.Repeat("b", 64)
	if d.cloudDraftMatchesSaved(saved) {
		t.Fatal("different registration inherited permissions")
	}
	d.modelAccountID = id
	draft := d.cloudSettingsDraft(saved, true)
	if draft.Provider != ProviderChatGPT || draft.AccountID != id || draft.Endpoint != "" || draft.WorkspaceID != "" || draft.CredentialRef != "" {
		t.Fatal("cloud draft leaked another provider route", draft)
	}
	d.modelProvider = ProviderClaude
	d.modelWorkspaceID = "workspace-2"
	d.modelAccountID = ""
	d.remoteAskEnabled = false
	d.remoteNotesEnabled = false
	draft = d.cloudSettingsDraft(saved, true)
	if draft.AccountID != "" || draft.WorkspaceID != "workspace-2" || draft.RemoteAskEnabled || draft.RemoteNotesEnabled {
		t.Fatal("Claude selection inherited ChatGPT grant", draft)
	}
}
func TestCloudSaveStateRequiresExplicitValidModelSelection(t *testing.T) {
	id := strings.Repeat("a", 64)
	d := &desktop{modelProvider: ProviderChatGPT, modelAccountID: id}
	d.cloudAI.accounts = []chatgpt.Account{{ID: id, Connected: true, InferenceEnabled: true}}
	if _, ready := d.localAIModelSaveState(LocalAISettings{}); ready {
		t.Fatal("connected account implicitly selected a model")
	}
	d.cloudAI.models = []cloudModelChoice{{ID: "chosen", Label: "Chosen"}}
	d.cloudAI.catalog = d.cloudCatalogIdentity()
	d.modelName = "chosen"
	if _, ready := d.localAIModelSaveState(LocalAISettings{}); !ready {
		t.Fatal("explicit entitled model cannot be saved")
	}
}

type fakeCloudLogin struct {
	account   chatgpt.Account
	cancelled bool
}

func (*fakeCloudLogin) AuthorizationURL() string {
	return "https://auth.openai.com/api/accounts/authorize?synthetic=1"
}
func (f *fakeCloudLogin) Wait(context.Context) (chatgpt.Account, error) { return f.account, nil }
func (f *fakeCloudLogin) Cancel()                                       { f.cancelled = true }

type fakeCloudAccounts struct {
	account chatgpt.Account
	flow    *fakeCloudLogin
}

func (f *fakeCloudAccounts) Accounts(context.Context) ([]chatgpt.Account, error) {
	return []chatgpt.Account{f.account}, nil
}
func (f *fakeCloudAccounts) BeginSignIn(context.Context, string) (cloudLoginFlow, error) {
	return f.flow, nil
}
func (*fakeCloudAccounts) ListModels(context.Context, string) ([]chatgpt.Model, error) {
	return nil, nil
}
func (*fakeCloudAccounts) Disconnect(context.Context, string) (bool, error) { return true, nil }
func TestConnectingChatGPTDoesNotSelectModelOrEnableDataPermissions(t *testing.T) {
	app := &pausedAskApp{posted: make(chan func(), 4)}
	account := chatgpt.Account{ID: strings.Repeat("a", 64), Label: "Fixture account", Connected: true, InferenceEnabled: true}
	manager := &fakeCloudAccounts{account: account, flow: &fakeCloudLogin{account: account}}
	opened := 0
	d := &desktop{app: app, modelProvider: ProviderChatGPT}
	d.cloudAI.accountsManager = manager
	d.cloudAI.openAuthorization = func(string) error { opened++; return nil }
	d.beginChatGPTSignIn("")
	(<-app.posted)()
	(<-app.posted)()
	if opened != 1 || d.modelAccountID != "" || d.modelName != "" || d.localAIEnabled || d.remoteAskEnabled || d.remoteNotesEnabled || len(d.cloudAI.accounts) != 1 {
		t.Fatal("connection changed model selection or data consent")
	}
}

type failingDisconnectAccounts struct {
	account chatgpt.Account
	calls   int
	err     error
}

func (f *failingDisconnectAccounts) Accounts(context.Context) ([]chatgpt.Account, error) {
	return []chatgpt.Account{f.account}, nil
}
func (*failingDisconnectAccounts) BeginSignIn(context.Context, string) (cloudLoginFlow, error) {
	return nil, chatgpt.ErrUnavailable
}
func (*failingDisconnectAccounts) ListModels(context.Context, string) ([]chatgpt.Model, error) {
	return nil, nil
}
func (f *failingDisconnectAccounts) Disconnect(context.Context, string) (bool, error) {
	f.calls++
	return false, f.err
}
func TestDisconnectUIReportsFailureWithoutClaimingLocalSignOut(t *testing.T) {
	for _, configureFails := range []bool{true, false} {
		t.Run(map[bool]string{true: "settings failure", false: "protected storage failure"}[configureFails], func(t *testing.T) {
			id := strings.Repeat("a", 64)
			manager := &failingDisconnectAccounts{account: chatgpt.Account{ID: id, Connected: true, InferenceEnabled: true}, err: chatgpt.ErrStorage}
			app := &pausedAskApp{posted: make(chan func(), 2)}
			d := &desktop{app: app, modelProvider: ProviderChatGPT, modelAccountID: id, localAIEnabled: true, remoteAskEnabled: true, remoteNotesEnabled: true}
			d.cloudAI.accountsManager = manager
			if configureFails {
				d.client = NewClient(nil)
				d.client.localAI = &localAIController{settings: LocalAISettings{Enabled: true, Provider: ProviderChatGPT, AccountID: id, Model: "chosen", RemoteAskEnabled: true, RemoteNotesEnabled: true}}
				t.Cleanup(d.client.Close)
			}
			d.disconnectChatGPTAccount(id)
			(<-app.posted)()
			if strings.Contains(d.cloudAI.status, "Signed out locally") || strings.Contains(d.cloudAI.status, "account disconnected") {
				t.Fatal("disconnect failure claimed sign out", d.cloudAI.status)
			}
			if configureFails && (manager.calls != 0 || !d.localAIEnabled || !d.remoteAskEnabled || !d.remoteNotesEnabled) {
				t.Fatal("failed settings save altered account/permissions")
			}
			if !configureFails && manager.calls != 1 {
				t.Fatal("disconnect not attempted")
			}
		})
	}
}

type delayedCloudAccounts struct {
	*fakeCloudAccounts
	entered, release chan struct{}
}

func (f *delayedCloudAccounts) BeginSignIn(context.Context, string) (cloudLoginFlow, error) {
	close(f.entered)
	<-f.release
	return f.flow, nil
}
func TestCancellingSignInDropsLateBrowserAndAccountResults(t *testing.T) {
	app := &pausedAskApp{posted: make(chan func(), 2)}
	account := chatgpt.Account{ID: strings.Repeat("a", 64), Connected: true, InferenceEnabled: true}
	manager := &delayedCloudAccounts{fakeCloudAccounts: &fakeCloudAccounts{account: account, flow: &fakeCloudLogin{account: account}}, entered: make(chan struct{}), release: make(chan struct{})}
	d := &desktop{app: app, modelProvider: ProviderChatGPT}
	d.cloudAI.accountsManager = manager
	opened := false
	d.cloudAI.openAuthorization = func(string) error { opened = true; return nil }
	d.beginChatGPTSignIn("")
	<-manager.entered
	d.cancelCloudSignIn()
	close(manager.release)
	(<-app.posted)()
	if opened || !manager.flow.cancelled || d.cloudAI.busy || len(d.cloudAI.accounts) != 0 {
		t.Fatal("cancelled sign-in opened browser or published account")
	}
}
func TestDashboardCloseCancelsPendingAccountOperation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d := &desktop{}
	d.cloudAI.cancel = cancel
	d.cloudAI.busy, d.cloudAI.login = true, true
	d.closeDashboard()
	if ctx.Err() == nil || d.cloudAI.busy || d.cloudAI.login {
		t.Fatal("dashboard close retained an account operation")
	}
}
