package durable

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hazcod/blur/internal/capture"
	"github.com/hazcod/blur/internal/chronicle"
	"github.com/hazcod/blur/internal/power"
)

// The oracle is the action ledger of actual inserted/deleted hourly inputs.
// No generated answer or production period-key helper supplies expected IDs.
// This tests runtime storage/worker transitions, not model prose quality.
func FuzzRollupWorkLifecycle(f *testing.F) {
	for _, seed := range [][]byte{{0}, {1, 0, 0, 4, 6, 5}, {29, 1, 7, 4, 3, 5}, {16, 0, 0, 0, 0, 6, 7, 5, 4}, {22, 1, 2, 5, 6, 0, 7}, {2, 4, 134, 0}, {3, 1, 4, 182, 5}, {16, 0, 4, 230, 1, 7}, {22, 4, 246, 0}, {2, 128, 136, 144, 152, 4, 7, 5}, {7, 144, 7, 4, 246, 152}, {26, 128, 136, 4, 182, 5}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		ctx := context.Background()
		path, key := durableTestPath(t), durableTestKey(t, 0xda)
		s, err := OpenDurable(path, key, CreateNew)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		zone := "UTC"
		setZone := func() { s.SetChronicleZone(func() string { return zone }) }
		setZone()
		type input struct {
			at     time.Time
			source string
		}
		live := map[string]input{}
		hoursSeen := map[string]bool{}
		periods := map[string]bool{}
		year := 1994 + int(data[0]%30)
		process := func() {
			w := chronicle.NewWorker(s, reviewFoundationPolicy{}, reviewFoundationModel{}, chronicle.WorkerConfig{Enabled: true, Provider: power.LocalProvider, Now: func() time.Time { return time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC) }, Zone: func() string { return zone }})
			err := w.ProcessRollups(ctx)
			w.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		insert := func(b byte, seq int) {
			at := time.Date(year, time.Month(1+int(b/8)%12), 1+int(b)%28, int(b)%24, 0, 0, 0, time.UTC)
			if b >= 128 {
				// Calendar-boundary extension: leap day, the following day,
				// and both sides of a local-zone year boundary.
				leap := 1996 + int(data[0]%7)*4
				at = []time.Time{
					time.Date(leap, 2, 29, 9, 0, 0, 0, time.UTC),
					time.Date(leap, 3, 1, 9, 0, 0, 0, time.UTC),
					time.Date(year, 12, 31, 23, 0, 0, 0, time.UTC),
					time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC),
				}[int(b/8)%4]
			}
			text := fmt.Sprintf("Synthetic release owner item%d generation%d at%s", b%8, seq, at.Format(time.RFC3339))
			apps := []string{"com.apple.mail", "com.slack.slack", "com.apple.Safari", "com.apple.Terminal"}
			o, err := capture.SanitizeObservation(apps[b%4], "", text, 1024)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.PutObservationAt(ctx, o, at.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			id := "hour:" + at.Format("20060102T15Z")
			r := chronicle.Summary{HourID: id, Start: at, End: at.Add(time.Hour), Text: text, SourceIDs: []string{o.ContentID()}}
			if err := s.PutChronicle(ctx, r); err != nil {
				t.Fatal(err)
			}
			live[id] = input{at, o.ContentID()}
			hoursSeen[id] = true
			// Track both independently chosen zones so obsolete periods must also
			// lose their content after a rebase or source deletion.
			for _, z := range []string{"UTC", "Europe/Amsterdam"} {
				loc, _ := time.LoadLocation(z)
				d := at.In(loc)
				periods["day:"+d.Format("2006-01-02")] = true
				periods["month:"+d.Format("2006-01")] = true
				periods["year:"+d.Format("2006")] = true
				y, w := d.ISOWeek()
				periods[fmt.Sprintf("week:%04d-W%02d", y, w)] = true
			}
		}
		insert(data[0], 0)
		for n, b := range data[1:min(len(data), 13)] {
			ids := make([]string, 0, len(live))
			for id := range live {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			switch b % 8 {
			case 0, 1:
				insert(b, n+1)
			case 2, 3:
				if len(ids) == 0 {
					continue
				}
				id := ids[int(b/8)%len(ids)]
				if b%8 == 2 {
					err = s.DeleteChronicle(ctx, id)
				} else {
					err = s.DeleteObservationRecord(ctx, live[id].source)
				}
				if err != nil {
					t.Fatal(err)
				}
				delete(live, id)
			case 4:
				process()
			case 5:
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = OpenDurable(path, key, OpenExisting)
				if err != nil {
					t.Fatal(err)
				}
				setZone()
			case 6:
				if b < 128 {
					if err := s.DeleteAllChronicleRollups(ctx); err != nil {
						t.Fatal(err)
					}
					break
				}
				// The high-bit extension commits intent, optionally interleaves
				// one quantum/source erasure, and restarts before model-free
				// recovery. Lower-byte actions keep their original meaning.
				hours := b&64 != 0
				if err := s.beginChronicleErasure(ctx, hours); err != nil {
					t.Fatal(err)
				}
				if b&16 != 0 && len(ids) != 0 {
					id := ids[int(b/8)%len(ids)]
					if err := s.DeleteObservationRecord(ctx, live[id].source); err != nil {
						t.Fatal(err)
					}
					delete(live, id)
				}
				if hours {
					clear(live)
				}
				if b&32 != 0 {
					if _, _, err := s.ResumeChronicleErasure(ctx); err != nil {
						t.Fatal(err)
					}
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = OpenDurable(path, key, OpenExisting)
				if err != nil {
					t.Fatal(err)
				}
				setZone()
				for quantum := 0; quantum < 64; quantum++ {
					_, more, err := s.ResumeChronicleErasure(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if !more {
						break
					}
					if quantum == 63 {
						t.Fatal("bounded model-free erasure failed to settle")
					}
				}
			case 7:
				if zone == "UTC" {
					zone = "Europe/Amsterdam"
				} else {
					zone = "UTC"
				}
			}
		}
		for pass := 0; pass < 40; pass++ {
			process()
			status, err := s.ChronicleRollupWorkStatus(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if status.Dirty == 0 && !status.BackfillPending {
				break
			}
			if pass == 39 {
				t.Fatalf("work did not settle: %+v", status)
			}
		}
		loc, _ := time.LoadLocation(zone)
		for id := range hoursSeen {
			row, found, err := s.GetChronicle(ctx, id)
			want, exists := live[id]
			if err != nil || found != exists || found && !slices.Equal(row.SourceIDs, []string{want.source}) {
				t.Fatalf("hour ledger mismatch after mutation/recovery: %s want%+v got%+v found%v err%v", id, want, row, found, err)
			}
		}
		wantSources, wantChildren := map[string][]string{}, map[string][]string{}
		for id, in := range live {
			d := in.at.In(loc)
			day := "day:" + d.Format("2006-01-02")
			wantSources[day] = append(wantSources[day], in.source)
			wantChildren[day] = append(wantChildren[day], id)
		}
		dayIDs := make([]string, 0, len(wantSources))
		for day := range wantSources {
			dayIDs = append(dayIDs, day)
		}
		for _, day := range dayIDs {
			sources := wantSources[day]
			d, _ := time.Parse("2006-01-02", day[len("day:"):])
			y, w := d.ISOWeek()
			week := fmt.Sprintf("week:%04d-W%02d", y, w)
			wantSources[week] = append(wantSources[week], sources...)
			wantChildren[week] = append(wantChildren[week], day)
			month := "month:" + d.Format("2006-01")
			wantSources[month] = append(wantSources[month], sources...)
			wantChildren[month] = append(wantChildren[month], day)
		}
		var monthIDs []string
		for id := range wantSources {
			if strings.HasPrefix(id, "month:") {
				monthIDs = append(monthIDs, id)
			}
		}
		for _, month := range monthIDs {
			year := "year:" + month[len("month:"):len("month:")+4]
			wantSources[year] = append(wantSources[year], wantSources[month]...)
			wantChildren[year] = append(wantChildren[year], month)
		}
		for id := range periods {
			got, found, err := s.GetChronicleRollup(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			want := wantSources[id]
			sort.Strings(want)
			want = slices.Compact(want)
			children := wantChildren[id]
			sort.Strings(children)
			if len(want) == 0 {
				if found && (got.Text != "" || got.Dirty || len(got.SourceIDs) != 0) {
					t.Fatalf("obsolete period retained evidence: %+v", got)
				}
				continue
			}
			if !found || got.Zone != zone || got.Dirty || got.Text == "" || got.Sampled || !slices.Equal(want, got.SourceIDs) || !slices.Equal(children, got.ChildIDs) {
				t.Fatalf("ledger mismatch period%s zone%s want sources%v children%v got%+v", id, zone, want, children, got)
			}
		}
	})
}
