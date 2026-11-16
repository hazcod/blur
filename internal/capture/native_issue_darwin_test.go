//go:build darwin

package capture

import (
	"context"
	"testing"
)

func TestNativeAXFailuresKeepOnlyStageAndStatus(t *testing.T) {
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		t.Fatal(nativeAX.err)
	}
	base := nativeAX.api
	for _, tc := range []struct {
		name  string
		stage ReadStage
		read  func(nativeAXAPI) error
	}{
		{"window", ReadStageWindow, func(api nativeAXAPI) error { _, err := focusedWindow(context.Background(), api, 7); return err }},
		{"role", ReadStageRole, func(api nativeAXAPI) error {
			_, err := (&nativeAXNode{api: api, element: 42}).role(context.Background())
			return err
		}},
		{"children", ReadStageChildren, func(api nativeAXAPI) error {
			_, _, err := (&nativeAXNode{api: api, element: 42}).children(context.Background(), 1)
			return err
		}},
		{"text", ReadStageText, func(api nativeAXAPI) error {
			_, err := (&nativeAXNode{api: api, element: 42}).value(context.Background(), 256)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := base
			api.createApp = func(int32) uintptr { return 42 }
			api.release = func(value uintptr) {
				if value != 42 {
					base.release(value)
				}
			}
			api.setTimeout = func(uintptr, float32) int32 { return axSuccess }
			api.copyValue = func(uintptr, uintptr, *uintptr) int32 { return -25204 }
			api.valueCount = func(uintptr, uintptr, *int64) int32 { return -25204 }
			err := tc.read(api)
			if got := ReadIssueForError(err); got != (ReadIssue{Stage: tc.stage, Status: -25204}) {
				t.Fatalf("issue=%+v err=%v", got, err)
			}
		})
	}
}

func TestNativeAXTimeoutFailureKeepsSetupStatus(t *testing.T) {
	api := nativeAXAPI{setTimeout: func(uintptr, float32) int32 { return -25211 }}
	err := (&nativeAXNode{api: api, element: 42}).prepare(context.Background())
	if got := ReadIssueForError(err); got != (ReadIssue{Stage: ReadStageSetup, Status: -25211}) {
		t.Fatalf("issue=%+v err=%v", got, err)
	}
}
