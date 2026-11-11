package windows

const (
	eventSystemForeground uint32 = 0x0003
	eventObjectFocus      uint32 = 0x8005
)

func winEventKind(event uint32, window uintptr) string {
	if window == 0 {
		return ""
	}
	switch event {
	case eventSystemForeground:
		return "frontmost_app_changed"
	case eventObjectFocus:
		return "focused_context_changed"
	default:
		return ""
	}
}
