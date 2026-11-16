//go:build !darwin

package darwin

func OpenMeetingAudioBridge(string) (*MeetingAudioBridge, error) {
	return nil, ErrMeetingAudioUnavailable
}
