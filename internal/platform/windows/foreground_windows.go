//go:build windows

package windows

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"unsafe"

	"github.com/hazcod/blur/internal/appidentity"
	"golang.org/x/sys/windows"
)

const (
	securityMandatoryHighRID = 0x3000
	desktopReadObjects       = 0x0001
	uoiName                  = 2
	maxBrowserWindowTitle    = 512
)

var (
	user32Desktop         = windows.NewLazySystemDLL("user32.dll")
	procOpenInputDesktop  = user32Desktop.NewProc("OpenInputDesktop")
	procCloseDesktop      = user32Desktop.NewProc("CloseDesktop")
	procGetUserObjectInfo = user32Desktop.NewProc("GetUserObjectInformationW")
	procGetWindowText     = user32Desktop.NewProc("GetWindowTextW")
)

func foregroundTarget(ctx context.Context) (FocusedTarget, error) {
	if ctx == nil {
		return FocusedTarget{}, errors.New("windows: foreground unavailable")
	}
	if err := ctx.Err(); err != nil {
		return FocusedTarget{}, err
	}
	hwnd := windows.GetForegroundWindow()
	if hwnd == 0 {
		return FocusedTarget{}, errors.New("windows: foreground unavailable")
	}
	var processID uint32
	threadID, err := windows.GetWindowThreadProcessId(hwnd, &processID)
	if err != nil || threadID == 0 || processID == 0 {
		return FocusedTarget{}, errors.New("windows: foreground unavailable")
	}
	if processID == windows.GetCurrentProcessId() {
		return FocusedTarget{}, errors.New("windows: own window excluded")
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, processID)
	if err != nil {
		return FocusedTarget{}, errors.New("windows: foreground unavailable")
	}
	defer windows.CloseHandle(process)

	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		return FocusedTarget{}, errors.New("windows: foreground unavailable")
	}
	creationTime := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	if creationTime == 0 {
		return FocusedTarget{}, errors.New("windows: foreground unavailable")
	}
	image := make([]uint16, 32768)
	imageSize := uint32(len(image))
	if err := windows.QueryFullProcessImageName(process, 0, &image[0], &imageSize); err != nil || imageSize == 0 || imageSize > uint32(len(image)) {
		return FocusedTarget{}, errors.New("windows: foreground unavailable")
	}
	appID := executableAppID(windows.UTF16ToString(image[:imageSize]))
	if appID == "" || len(appID) > 128 {
		return FocusedTarget{}, errors.New("windows: foreground unavailable")
	}

	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return FocusedTarget{}, errors.New("windows: foreground unavailable")
	}
	defer token.Close()
	elevated, err := highIntegrity(token)
	if err != nil {
		return FocusedTarget{}, errors.New("windows: foreground unavailable")
	}
	secure := onSecureDesktop()
	private := false
	if appidentity.IsBrowserApp(appID) {
		title, titleErr := foregroundWindowTitle(uintptr(hwnd))
		private = isPrivateBrowserWindow(appID, title, titleErr)
	}
	if err := ctx.Err(); err != nil {
		return FocusedTarget{}, err
	}
	if windows.GetForegroundWindow() != hwnd {
		return FocusedTarget{}, errors.New("windows: foreground changed")
	}
	return FocusedTarget{
		Window: uintptr(hwnd), ProcessID: processID, AppID: strings.ToLower(appID),
		CreationTime: creationTime, Elevated: elevated, Secure: secure, Private: private,
	}, nil
}

func foregroundWindowTitle(hwnd uintptr) (string, error) {
	if hwnd == 0 {
		return "", errors.New("windows: foreground title unavailable")
	}
	buffer := make([]uint16, maxBrowserWindowTitle+1)
	read, _, _ := procGetWindowText.Call(hwnd, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	runtime.KeepAlive(buffer)
	if read > uintptr(maxBrowserWindowTitle) {
		return "", errors.New("windows: foreground title unavailable")
	}
	return decodeForegroundWindowTitle(buffer, int(read))
}

func highIntegrity(token windows.Token) (bool, error) {
	var needed uint32
	err := windows.GetTokenInformation(token, windows.TokenIntegrityLevel, nil, 0, &needed)
	if err != windows.ERROR_INSUFFICIENT_BUFFER || needed < uint32(unsafe.Sizeof(windows.Tokenmandatorylabel{})) || needed > 4096 {
		return false, errors.New("windows: token unavailable")
	}
	buffer := make([]byte, needed)
	if err := windows.GetTokenInformation(token, windows.TokenIntegrityLevel, &buffer[0], uint32(len(buffer)), &needed); err != nil {
		return false, errors.New("windows: token unavailable")
	}
	label := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&buffer[0]))
	if label.Label.Sid == nil {
		return false, errors.New("windows: token unavailable")
	}
	count := label.Label.Sid.SubAuthorityCount()
	if count == 0 {
		return false, errors.New("windows: token unavailable")
	}
	return label.Label.Sid.SubAuthority(uint32(count-1)) >= securityMandatoryHighRID, nil
}

// Secure desktops such as Winlogon are never readable. Any uncertainty about
// the active input desktop is treated as secure rather than guessed open.
func onSecureDesktop() bool {
	desktop, _, _ := procOpenInputDesktop.Call(0, 0, desktopReadObjects)
	if desktop == 0 {
		return true
	}
	defer procCloseDesktop.Call(desktop)
	name := make([]uint16, 256)
	var needed uint32
	result, _, _ := procGetUserObjectInfo.Call(desktop, uintptr(uoiName), uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)*2), uintptr(unsafe.Pointer(&needed)))
	if result == 0 || needed == 0 || needed > uint32(len(name)*2) {
		return true
	}
	desktopName := windows.UTF16ToString(name)
	return !strings.EqualFold(desktopName, "Default")
}
