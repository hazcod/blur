package darwin

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestScreenTextRegionReplyRequiresExactEchoAndWindowContainment(t *testing.T) {
	region := ScreenTextRect{X: 10, Y: 20, Width: 60, Height: 50}
	request := screenTextWireRequest{Version: 1, Kind: "recognize", RequestID: "region", PID: 7, WindowID: 42, LeaseToken: strings.Repeat("a", 64), MaxTextBytes: 4096, TimeoutMS: 1000, Region: &region}
	text := "Only page text"
	window := ScreenTextWindow{PID: 7, WindowID: 42, BundleID: "test.synthetic", Title: "Synthetic", TitleAvailable: true, Frame: ScreenTextRect{Width: 100, Height: 100}, FullOnscreen: true, Unobscured: true, PermissionGranted: true, LeaseToken: request.LeaseToken}
	valid := screenTextWireReply{Version: 1, RequestID: request.RequestID, PID: 7, WindowID: 42, CapturedAtMilliseconds: 1, Status: "ok", Window: &window, Text: &text, Lines: []ScreenTextLine{{Text: text, Confidence: 0.9, Rect: ScreenTextRect{Width: 1, Height: 1}}}, Region: &region}
	for _, scenario := range []string{"success", "missing", "changed", "outside", "unsolicited"} {
		t.Run(scenario, func(t *testing.T) {
			req, reply := request, valid
			r := region
			switch scenario {
			case "missing":
				reply.Region = nil
			case "changed":
				r.X++
				reply.Region = &r
			case "outside":
				r.Width = 101
				req.Region, reply.Region = &r, &r
			case "unsolicited":
				req.Region = nil
			}
			data, err := json.Marshal(reply)
			if err != nil {
				t.Fatal(err)
			}
			got, err := decodeScreenTextReply(data, req)
			if scenario == "success" {
				if err != nil || got.Region == nil || *got.Region != region {
					t.Fatal("exact crop authority lost", err)
				}
			} else if !errors.Is(err, ErrScreenTextInvalid) {
				t.Fatal("uncropped/mismatched region accepted", err)
			}
		})
	}
}

func TestScreenTextInvalidRegionNeverCrossesNativeBoundary(t *testing.T) {
	bridge := newScreenTextBridge(screenTextNativeAPI{start: func(*byte, int32) uint64 { t.Fatal("invalid crop reached native acquisition"); return 0 }})
	for _, region := range []ScreenTextRect{{Width: 0, Height: 10}, {Width: -1, Height: 10}, {X: math.NaN(), Width: 10, Height: 10}, {Y: math.Inf(1), Width: 10, Height: 10}, {Width: 32769, Height: 10}} {
		_, err := bridge.RecognizeWindow(context.Background(), ScreenTextRequest{RequestID: "region", PID: 7, WindowID: 42, LeaseToken: strings.Repeat("a", 64), Region: &region})
		if !errors.Is(err, ErrScreenTextInvalid) {
			t.Fatal("invalid crop accepted", err)
		}
	}
}
