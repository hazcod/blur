package darwin

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/ebitengine/purego"
)

var meetingAudioLibraries struct {
	sync.Mutex
	byPath map[string]meetingAudioNativeAPI
}

// OpenMeetingAudioBridge loads an explicitly selected bundled native bridge. It
// never searches the network, PATH, environment variables or other app bundles.
// Bundle construction must sign this library with Blur's existing certificate.
func OpenMeetingAudioBridge(path string) (*MeetingAudioBridge, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrMeetingAudioInvalid
	}
	// The caller selects Blur's bundled native library, not a path from meeting
	// content. Absolute/canonical validation above rejects traversal syntax;
	// Hardened Runtime library validation enforces the signing trust boundary.
	info, err := os.Stat(path) // #nosec G703 -- Intentionally stat the caller-selected, absolute bundled FFI library; no file contents are read here.
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrMeetingAudioUnavailable
	}
	meetingAudioLibraries.Lock()
	defer meetingAudioLibraries.Unlock()
	if api, ok := meetingAudioLibraries.byPath[path]; ok {
		return newMeetingAudioBridge(api), nil
	}
	library, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, ErrMeetingAudioUnavailable
	}
	open, err := purego.Dlsym(library, "blur_meeting_audio_open")
	if err != nil {
		return nil, ErrMeetingAudioUnavailable
	}
	poll, err := purego.Dlsym(library, "blur_meeting_audio_poll")
	if err != nil {
		return nil, ErrMeetingAudioUnavailable
	}
	cancel, err := purego.Dlsym(library, "blur_meeting_audio_cancel")
	if err != nil {
		return nil, ErrMeetingAudioUnavailable
	}
	var api meetingAudioNativeAPI
	control, err := purego.Dlsym(library, "blur_meeting_audio_control")
	if err != nil {
		return nil, ErrMeetingAudioUnavailable
	}
	purego.RegisterFunc(&api.open, open)
	purego.RegisterFunc(&api.control, control)
	purego.RegisterFunc(&api.poll, poll)
	purego.RegisterFunc(&api.cancel, cancel)
	if meetingAudioLibraries.byPath == nil {
		meetingAudioLibraries.byPath = make(map[string]meetingAudioNativeAPI)
	}
	meetingAudioLibraries.byPath[path] = api
	return newMeetingAudioBridge(api), nil
}
