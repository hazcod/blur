package assistant

import "testing"

func TestCalendarLayersPreserveDigestPriorityAndExistingValues(t *testing.T) {
	// Existing stored enum values must not shift when calendar layers append.
	if LayerSource != 0 || LayerHour != 1 || LayerDay != 2 || LayerWeek != 3 || LayerDigest != 4 {
		t.Fatal("existing evidence layer values changed")
	}
	items := []Evidence{{Layer: Layer(5)}, {Layer: LayerDigest}, {Layer: Layer(6)}, {Layer: LayerWeek}}
	ordered := layerOrder(items)
	if ordered[0].Layer != LayerDigest || ordered[1].Layer != Layer(6) || ordered[2].Layer != Layer(5) || layerName(Layer(5)) != "month" || layerName(Layer(6)) != "year" {
		t.Fatalf("calendar layers lost source type or authored-digest priority: %+v", ordered)
	}
}
