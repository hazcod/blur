package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"

	"github.com/hazcod/blur/internal/deletion"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/darwin"
	"github.com/hazcod/blur/internal/replica"
	"github.com/hazcod/blur/internal/storage/durable"
)

// replicaKeySecret names the derived sync key in the OS secret store. It is
// never the device store key.
const replicaKeySecret = "replica-sync-key"

// replicaService wires the replica package to the app: state in
// <data dir>/replica/state.json, the derived key in the OS secret store and
// the sync folder behind a security-scoped bookmark.
type replicaService struct {
	state   *replica.StateFile
	runner  *replica.Runner
	secrets platform.SecretStore
	// notify posts one UI update; set when the Privacy page first renders.
	notify atomic.Pointer[func()]
}

// passDone is the runner's OnPass hook: it invalidates the UI once so the
// "Last backup" line updates without user interaction.
func (s *replicaService) passDone() {
	if f := s.notify.Load(); f != nil {
		(*f)()
	}
}

// snapshotMode reports whether the UI snapshot run (throwaway store) is
// active; the runner must never start then.
func snapshotMode() bool { return uiSnapshotDir() != "" }

func newReplicaService(directory string, store *durable.Store, deleter *deletion.Service, client *Client, secrets platform.SecretStore) (*replicaService, error) {
	state, err := replica.OpenState(filepath.Join(directory, "replica", "state.json"))
	if err != nil {
		return nil, err
	}
	s := &replicaService{state: state, secrets: secrets}
	fence := func() (func() bool, bool) {
		epoch, stable := client.BeginMemoryRead()
		return func() bool { return client.MemoryReadCurrent(epoch) }, stable
	}
	s.runner = &replica.Runner{
		OnPass:   s.passDone,
		Writer:   &replica.Writer{Store: store, State: state, Resolve: s.resolve, Fence: fence},
		Importer: &replica.Importer{Store: store, Deleter: deleter, State: state, Resolve: s.resolve, Fence: fence, Changing: client.NotifyMemoryChanging, Stable: client.NotifyMemoryStable},
	}
	return s, nil
}

// resolve opens the chosen folder and loads the derived key.
func (s *replicaService) resolve(ctx context.Context) (replica.Target, error) {
	st := s.state.Get()
	if st.FolderPath == "" {
		return replica.Target{}, replica.ErrFolderNotReady
	}
	if !st.KeySet || s.secrets == nil {
		return replica.Target{}, replica.ErrNoKey
	}
	key, err := s.secrets.Get(ctx, replicaKeySecret, replica.KeySize+1)
	if err != nil || len(key) != replica.KeySize {
		clear(key)
		return replica.Target{}, replica.ErrNoKey
	}
	dir, release, err := s.openFolder(st)
	if err != nil {
		clear(key)
		return replica.Target{}, err
	}
	if _, err := replica.LoadOrCreateRoot(dir); err != nil {
		release()
		clear(key)
		return replica.Target{}, err
	}
	return replica.Target{Dir: dir, Key: key, Release: release}, nil
}

// openFolder resolves the saved bookmark (or the plain path when there is
// none) and returns a release func that ends security-scoped access.
func (s *replicaService) openFolder(st replica.State) (string, func(), error) {
	if len(st.Bookmark) == 0 {
		return st.FolderPath, func() {}, nil
	}
	path, release, stale, err := darwin.ResolveBookmark(st.Bookmark)
	if err != nil {
		return "", nil, replica.ErrFolderNotReady
	}
	if stale {
		// Re-create from the resolved path while access is active. A failure
		// keeps the old bookmark, which still resolves, and retries next time.
		if fresh, berr := darwin.CreateBookmark(path); berr == nil {
			old := st.Bookmark
			if uerr := s.state.Update(func(cur *replica.State) {
				if bytes.Equal(cur.Bookmark, old) && cur.FolderPath == st.FolderPath {
					cur.Bookmark = fresh
				}
			}); uerr != nil {
				slog.Warn("sync bookmark refresh failed", "result", "failed")
			}
		} else {
			slog.Warn("sync bookmark refresh failed", "result", "failed")
		}
	}
	return path, release, nil
}

func (s *replicaService) start() {
	if s != nil && s.state.Get().Enabled && !snapshotMode() {
		s.runner.Start(context.Background())
	}
}

// Close flushes pending changes once and stops the schedule. Call before the
// store is closed.
func (s *replicaService) Close() {
	if s == nil {
		return
	}
	s.notify.Store(nil) // the UI is going away; no more refresh requests
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = s.runner.FlushNow(ctx)
	cancel()
	s.runner.Close()
}

// flushAsync writes pending changes without blocking the caller (capture
// pause). Failures are logged by the writer content-free.
func (s *replicaService) flushAsync() {
	if s == nil || !s.state.Get().Enabled {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.runner.FlushNow(ctx)
	}()
}

func (s *replicaService) setEnabled(enabled bool) error {
	if err := s.state.Update(func(st *replica.State) { st.Enabled = enabled }); err != nil {
		return err
	}
	if enabled && !snapshotMode() {
		s.runner.Start(context.Background())
		s.runner.Poke()
	}
	return nil
}

// setFolder stores the chosen folder. A different folder has a different salt
// and a fresh history, so the key and export progress reset.
func (s *replicaService) setFolder(path string, bookmark []byte) error {
	previous := s.state.Get()
	changed := previous.FolderPath != path
	if err := s.state.Update(func(st *replica.State) {
		st.FolderPath, st.Bookmark = path, bookmark
		if changed {
			st.KeySet = false
			st.ObsWatermark, st.TombWatermark, st.LastSeq = 0, 0, 0
			st.Imported, st.OtherDevices, st.LastBackupAt = nil, 0, time.Time{}
		}
	}); err != nil {
		return err
	}
	if changed && previous.KeySet && s.secrets != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.secrets.Delete(ctx, replicaKeySecret)
		cancel()
	}
	return nil
}

// setPassphrase derives the key from the folder salt and stores it. The
// passphrase itself is never persisted.
func (s *replicaService) setPassphrase(ctx context.Context, passphrase string) error {
	st := s.state.Get()
	if st.FolderPath == "" {
		return replica.ErrFolderNotReady
	}
	if s.secrets == nil {
		return replica.ErrNoKey
	}
	dir, release, err := s.openFolder(st)
	if err != nil {
		return err
	}
	defer release()
	root, err := replica.LoadOrCreateRoot(dir)
	if err != nil {
		return err
	}
	key, err := replica.DeriveKey(passphrase, root.Salt)
	if err != nil {
		return err
	}
	defer clear(key)
	if err := s.secrets.Put(ctx, replicaKeySecret, key); err != nil {
		return err
	}
	return s.state.Update(func(st *replica.State) { st.KeySet = true })
}

// flushReplica writes pending sync changes when capture is paused.
func (c *Client) flushReplica() {
	if c != nil {
		c.replica.flushAsync()
	}
}

// syncSettingsState is the Settings page's draft state for "Sync across Macs".
type syncSettingsState struct {
	passphrase string
	message    string
	busy       bool
}

// syncStatusLine summarizes sync for the Settings page. No content, only times
// and counts.
func syncStatusLine(st replica.State) string {
	if !st.Enabled {
		return "Off."
	}
	if st.FolderPath == "" {
		return "On. Choose a folder to start."
	}
	if !st.KeySet {
		return "On. Set a passphrase to start."
	}
	backup := "No backup yet."
	if !st.LastBackupAt.IsZero() {
		backup = "Last backup " + st.LastBackupAt.In(time.Local).Format("15:04") + "."
	}
	return backup + " " + otherDevicesLabel(st.OtherDevices)
}

func otherDevicesLabel(n int) string {
	switch n {
	case 0:
		return "No other devices."
	case 1:
		return "1 other device."
	default:
		return strconv.Itoa(n) + " other devices."
	}
}

// syncActionMessage maps a failure to short, content-free guidance.
func syncActionMessage(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, replica.ErrNoKey):
		return "Set a passphrase first."
	case errors.Is(err, replica.ErrFolderNotReady):
		return "The sync folder is not available yet. Wait for it to finish syncing, then try again."
	case errors.Is(err, replica.ErrAuth):
		return "Another device uses a different passphrase, or a file was altered."
	case errors.Is(err, replica.ErrMemoryChanging):
		return "Memory is being changed. Try again in a moment."
	default:
		return "Sync could not finish. Blur will retry."
	}
}

// syncSettingsViews returns the "Sync across Macs" group, or nothing when this
// build has no replica service.
func (d *desktop) syncSettingsViews() []ui.View {
	if d == nil || d.client == nil || d.client.replica == nil {
		return nil
	}
	return []ui.View{d.syncSettingsView()}
}

func (d *desktop) syncSettingsView() ui.View {
	if d.sync == nil {
		d.sync = &syncSettingsState{}
	}
	service := d.client.replica
	if service.notify.Load() == nil {
		app := d.app
		hook := func() { app.Post(app.RequestUpdate) }
		service.notify.Store(&hook)
	}
	st := service.state.Get()
	rows := []ui.View{
		wrapLabel("Sync across Macs").Style(styleSectionTitle),
		wrapLabel("Blur writes sealed, encrypted change files to a folder that iCloud Drive or another sync service carries, and imports the files your other Macs write. Activity and time recaps stay on each Mac.").Style(styleMeta),
		wrapLabel(syncStatusLine(st)).Style(modern.MutedText),
	}
	toggle := primaryButton("Turn on")
	if st.Enabled {
		toggle = secondaryButton("Turn off")
	}
	folderLabel := "Choose folder…"
	if st.FolderPath != "" {
		folderLabel = "Change folder…"
	}
	rows = append(rows, ui.HBox(
		toggle.Enabled(!d.sync.busy).OnClick(func() { d.syncToggle(!st.Enabled) }),
		flatButton(folderLabel).Enabled(!d.sync.busy).OnClick(d.syncChooseFolder),
	).Spacing(8))
	if st.FolderPath != "" {
		rows = append(rows, wrapLabel(st.FolderPath).Style(styleMeta))
	}
	passphraseButton := secondaryButton("Set passphrase")
	if st.KeySet {
		rows = append(rows, wrapLabel("Passphrase set").Style(styleMeta))
	}
	rows = append(rows,
		ui.TextInput().Text(d.sync.passphrase).Placeholder("Sync passphrase (same on every Mac)").Obscured(true).Enabled(!d.sync.busy && st.FolderPath != "").OnText(func(value string) {
			d.sync.passphrase = value
			d.app.RequestUpdate()
		}),
		passphraseButton.Enabled(!d.sync.busy && st.FolderPath != "" && d.sync.passphrase != "").OnClick(d.syncSetPassphrase),
		secondaryButton("Import now").Enabled(!d.sync.busy && st.Enabled && st.KeySet).OnClick(d.syncImportNow),
	)
	if d.sync.message != "" {
		rows = append(rows, wrapLabel(d.sync.message).Style(modern.MutedText))
	}
	return surfaceBox(14, 8, hugButtons(rows)...)
}

// syncFinish clears the busy flag and shows a content-free message.
func (d *desktop) syncFinish(err error, done string) {
	d.app.Post(func() {
		d.sync.busy = false
		d.sync.message = done
		if err != nil {
			d.sync.message = syncActionMessage(err)
		}
		d.app.RequestUpdate()
	})
}

func (d *desktop) syncToggle(enable bool) {
	service := d.client.replica
	if err := service.setEnabled(enable); err != nil {
		slog.Warn("sync toggle failed", "result", "failed")
		d.sync.message = syncActionMessage(err)
	} else {
		d.sync.message = ""
	}
	d.app.RequestUpdate()
}

// syncChooseFolder runs on the AppKit main thread (a button click).
func (d *desktop) syncChooseFolder() {
	service := d.client.replica
	home, _ := os.UserHomeDir()
	path, bookmark, err := darwin.ChooseFolder(darwin.DefaultFolderStart(home, service.state.Get().FolderPath))
	switch {
	case errors.Is(err, darwin.ErrFolderPickerCancelled):
		return
	case err != nil:
		d.sync.message = "The folder could not be opened. Choose another one."
	default:
		if err := service.setFolder(path, bookmark); err != nil {
			d.sync.message = syncActionMessage(err)
		} else {
			d.sync.message = "Folder chosen. Set the passphrase next."
		}
	}
	d.app.RequestUpdate()
}

func (d *desktop) syncSetPassphrase() {
	service := d.client.replica
	passphrase := d.sync.passphrase
	d.sync.passphrase, d.sync.busy, d.sync.message = "", true, "Setting passphrase…"
	d.app.RequestUpdate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := service.setPassphrase(ctx, passphrase)
		passphrase = ""
		if err == nil {
			service.runner.Poke()
		}
		d.syncFinish(err, "Passphrase saved.")
	}()
}

func (d *desktop) syncImportNow() {
	service := d.client.replica
	d.sync.busy, d.sync.message = true, "Importing…"
	d.app.RequestUpdate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		res, err := service.runner.ImportNow(ctx)
		d.syncFinish(err, fmt.Sprintf("Imported %d new items.", res.Observations+res.Tombstones))
	}()
}
