package hnsw

import (
	"math"
	"math/rand"
	"testing"
)

func TestGraphRecallAndReuseAfterRemovingEveryNode(t *testing.T) {
	graph := NewGraph[int]()
	graph.Rng = rand.New(rand.NewSource(7))
	graph.EfSearch = 128
	const count = 400
	for i := 0; i < count; i++ {
		angle := 2 * math.Pi * float64(i) / count
		graph.Add(MakeNode(i, []float32{float32(math.Cos(angle)), float32(math.Sin(angle))}))
	}
	for i := 0; i < count; i += 11 {
		vector, _ := graph.Lookup(i)
		hits := graph.SearchWithDistance(vector, 8)
		if len(hits) != 8 || hits[0].Key != i || hits[0].Distance > .0001 {
			t.Fatalf("nearest source %d: %+v", i, hits)
		}
	}
	for i := 0; i < count; i++ {
		if !graph.Delete(i) {
			t.Fatalf("node %d not removed", i)
		}
	}
	graph.Add(MakeNode(500, []float32{1, 0}))
	hits := graph.Search([]float32{1, 0}, 1)
	if len(hits) != 1 || hits[0].Key != 500 || graph.Len() != 1 {
		t.Fatalf("empty graph reuse failed: %+v", hits)
	}
}

func TestPortableCosineDistance(t *testing.T) {
	for _, tc := range []struct {
		a, b []float32
		want float32
	}{
		{[]float32{3, 4}, []float32{6, 8}, 0},
		{[]float32{1, 0}, []float32{0, 1}, 1},
		{[]float32{1, 0}, []float32{-1, 0}, 2},
	} {
		if distance := CosineDistance(tc.a, tc.b); math.Abs(float64(distance-tc.want)) > .00001 {
			t.Fatalf("distance=%f want=%f", distance, tc.want)
		}
	}
}
