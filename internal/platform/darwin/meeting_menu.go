package darwin

// MeetingMenuControls describes acknowledged recording state and its explicit
// controls. Merely opening or rebuilding a menu never changes audio state.
type MeetingMenuControls struct {
	Active bool
	Paused bool
	Busy   bool
	Stop   func()
}
