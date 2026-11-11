package capture

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/fake"
)

// BenchmarkCaptureSlackLoadedAccessibilityTree measures CPU and allocations
// for a loaded Slack-like accessibility tree, including channel navigation,
// message authors/timestamps, message bodies, chrome filtering, validation,
// flattening, and redaction. It does not include native OS API latency.
func BenchmarkCaptureSlackLoadedAccessibilityTree(b *testing.B) {
	const channels = 120
	const messages = 360

	roots := make([]*rawNode, 0, channels+messages+1)
	toolbar := &rawNode{role: "AXGroup", children: make([]*rawNode, 0, 20)}
	for index := range 20 {
		label := fmt.Sprintf("toolbar control %02d", index)
		toolbar.children = append(toolbar.children, benchmarkAXValue("AXButton", label))
	}
	roots = append(roots, toolbar)

	navigation := &rawNode{role: "AXGroup", children: make([]*rawNode, 0, channels)}
	for index := range channels {
		channel := fmt.Sprintf("# project-%03d", index)
		navigation.children = append(navigation.children, benchmarkAXValue("AXStaticText", channel))
	}
	roots = append(roots, navigation)

	conversation := &rawNode{role: "AXGroup", children: make([]*rawNode, 0, messages)}
	for index := range messages {
		message := &rawNode{role: "AXGroup", children: make([]*rawNode, 0, 3)}
		message.children = append(message.children,
			benchmarkAXValue("AXStaticText", fmt.Sprintf("Person %02d", index%24)),
			benchmarkAXValue("AXStaticText", fmt.Sprintf("10:%02d AM", index%60)),
			benchmarkAXValue("AXStaticText", fmt.Sprintf("Loaded off-screen discussion item %03d about the release checklist and owner follow-up.", index)),
		)
		conversation.children = append(conversation.children, message)
	}
	roots = append(roots, conversation)

	frame := rawFrame{roots: roots}
	source := &fake.CaptureSource{
		Report:      platform.CapabilityReport{State: platform.Available, Reason: "ready", Evidence: "fixture"},
		ProbeResult: platform.CaptureProbe{TargetID: "capture-benchmark", AppID: "com.tinyspeck.slackmacgap"},
	}
	pipeline := Pipeline{
		Source: source,
		Limits: DefaultLimits(),
		Read: func(context.Context, platform.CaptureProbe, Limits) (rawFrame, error) {
			return frame, nil
		},
	}

	baseline, err := pipeline.Capture(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	if got, want := strings.Count(baseline.Text(), "Loaded off-screen discussion item"), messages; got != want {
		b.Fatalf("benchmark fixture retained %d of %d loaded message bodies", got, want)
	}

	b.ReportAllocs()
	b.SetBytes(int64(len(baseline.Text())))
	b.ResetTimer()
	for range b.N {
		if _, err := pipeline.Capture(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkAXValue(role, text string) *rawNode {
	value := text
	return &rawNode{
		role: role, declaredBytes: len(value),
		value: func(context.Context, int) (string, error) { return value, nil },
	}
}
