package client

import (
	"fmt"
	"strings"

	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/enrich"
)

type libraryFactGroup struct {
	title string
	state knowledge.State
	facts []knowledge.Fact
}

// libraryDetailKey folds sentence-ending punctuation for presentation-level
// deduplication without collapsing meaningful symbols such as C++, C#, or
// dotted identifiers. The stored fact text remains untouched.
func libraryDetailKey(text string) string {
	return knowledge.NormalizeExact(strings.TrimRight(strings.TrimSpace(text), " \t\r\n.,;:!?…\"”’)]}"))
}

func librarySubjectFactGroups(entity knowledge.Entity) []libraryFactGroup {
	groups := []libraryFactGroup{{title: "Confirmed details", state: knowledge.Confirmed}, {title: "From saved sources", state: knowledge.Deterministic}, {title: "Inferred details to review", state: knowledge.Proposed}}
	for _, fact := range entity.Facts {
		if strings.TrimSpace(fact.Text) == "" {
			continue
		}
		switch fact.State {
		case knowledge.Confirmed:
			groups[0].facts = append(groups[0].facts, fact)
		case knowledge.Deterministic:
			groups[1].facts = append(groups[1].facts, fact)
		case knowledge.Proposed:
			groups[2].facts = append(groups[2].facts, fact)
		}
	}
	return groups
}

// Readable subject content comes from saved facts. Rendering never generates a
// new claim or promotes a proposed detail to confirmed knowledge.
func (d *desktop) librarySubjectAccount(entity knowledge.Entity, detailed bool) []ui.View {
	var rows []ui.View
	total, shown := 0, 0
	for _, group := range librarySubjectFactGroups(entity) {
		total += len(group.facts)
		if len(group.facts) == 0 || !detailed && shown >= 4 {
			continue
		}
		facts := group.facts
		if !detailed {
			facts = facts[:min(len(facts), 4-shown)]
		}
		groupRows := []ui.View{recapSectionTitle(group.title)}
		if group.state == knowledge.Proposed {
			groupRows = append(groupRows, wrapLabel("Inferred from saved sources; not confirmed.").Style(styleMeta))
		}
		for _, fact := range facts {
			content := []ui.View{wrapLabel(fact.Text)}
			content = append(content, d.libraryFactSources(entity, fact, detailed)...)
			if detailed && enrich.IsEntityID(entity.ID) {
				content = append(content, d.libraryFactControls(entity.ID, fact))
			}
			groupRows = append(groupRows, ui.VBox(content...).Spacing(4))
			shown++
		}
		rows = append(rows, ui.VBox(groupRows...).Spacing(8))
	}
	if total == 0 {
		rows = append(rows, wrapLabel("No learned details about "+entity.Name+" yet.").Style(modern.MutedText))
		if len(entity.ActivitySourceIDs) == 0 {
			rows = append(rows, wrapLabel("Only the subject name has been saved.").Style(styleMeta))
		} else {
			rows = append(rows, wrapLabel("Linked sources are saved, but no details have been extracted yet.").Style(styleMeta))
			if d.client != nil {
				rows = append(rows, wrapLabel(libraryStatusCopy(d.client.LibraryStatus(), effectiveProvider(d.client.LocalAISettings()))).Style(styleMeta))
			}
		}
	}
	if !detailed && shown < total {
		rows = append(rows, flatButton(fmt.Sprintf("Read all %d details", total)).Enabled(!d.loading && !d.libraryInspectLoading).OnClick(func() { d.openLibrarySubject(entity, "") }))
	}
	var loops []ui.View
	for _, loop := range entity.OpenLoops {
		if loop.State == knowledge.Dismissed || strings.TrimSpace(loop.Text) == "" {
			continue
		}
		text := loop.Text
		if loop.State == knowledge.Proposed {
			text += " (inferred, not confirmed)"
		}
		loops = append(loops, wrapLabel(text))
	}
	if len(loops) > 0 {
		rows = append(rows, recapSectionTitle("Open questions"))
		rows = append(rows, loops...)
	}
	return rows
}

func (d *desktop) libraryFactSources(entity knowledge.Entity, fact knowledge.Fact, detailed bool) []ui.View {
	var rows []ui.View
	for i, source := range fact.Sources {
		if !detailed && i >= 3 {
			rows = append(rows, flatButton("View all sources").Enabled(!d.loading && !d.libraryInspectLoading).OnClick(func() { d.openLibrarySubject(entity, "") }))
			break
		}
		if source.UserAuthored {
			label := "Your note"
			if enrich.IsEntityID(entity.ID) {
				label = "Your correction"
			}
			rows = append(rows, wrapLabel(label).Style(styleMeta))
			continue
		}
		if source.ID == "" {
			continue
		}
		id := source.ID
		label := "View source"
		if len(fact.Sources) > 1 {
			label = fmt.Sprintf("View source %d", i+1)
		}
		rows = append(rows, flatButton(label).Enabled(d.client != nil && d.client.CanViewChronicles() && !d.loading && !d.libraryInspectLoading).OnClick(func() { d.openLibrarySubject(entity, id) }))
	}
	return rows
}

func (d *desktop) libraryFactControls(entityID string, fact knowledge.Fact) ui.View {
	factID := fact.ID
	if d.libraryEditFact == fact.ID {
		return ui.VBox(
			ui.TextInput().Text(d.libraryEditText).OnText(func(text string) { d.libraryEditText = text; d.app.RequestUpdate() }),
			ui.HBox(primaryButton("Save correction").Enabled(!d.loading && enrich.ValidCorrection(strings.TrimSpace(d.libraryEditText))).OnClick(func() { d.saveLibraryFact(entityID, factID, false) }), flatButton("Cancel edit").OnClick(func() { d.libraryEditFact = ""; d.app.RequestUpdate() })).Spacing(8),
		).Spacing(6)
	}
	controls := []ui.View{}
	if fact.State == knowledge.Proposed {
		controls = append(controls, flatButton("Confirm").Enabled(!d.loading).OnClick(func() { d.confirmLibraryFact(entityID, fact) }))
	}
	controls = append(controls,
		flatButton("Correct").Enabled(!d.loading).OnClick(func() { d.libraryEditFact = factID; d.libraryEditText = fact.Text; d.app.RequestUpdate() }),
		flatButton("Dismiss memory").Enabled(!d.loading).OnClick(func() { d.saveLibraryFact(entityID, factID, true) }),
	)
	return ui.HBox(controls...).Spacing(8)
}

// A source link opens the account it supports, then reads the captured evidence
// with the existing memory epoch and request fences.
func (d *desktop) openLibrarySubject(entity knowledge.Entity, sourceID string) {
	if d.loading || d.libraryInspectLoading {
		return
	}
	if sourceID != "" && d.libraryInspect != nil && d.libraryInspect.ID == entity.ID {
		d.inspectLibrarySource(sourceID)
		return
	}
	if sourceID == "" && enrich.IsEntityID(entity.ID) {
		d.inspectLibraryEntity(entity.ID, 0)
		return
	}
	copy := knowledge.Clone(entity)
	d.libraryInspect = &copy
	d.librarySource = nil
	d.libraryRelated = nil
	d.libraryRelatedOffset = 0
	d.libraryEditFact = ""
	if sourceID != "" {
		d.inspectLibrarySource(sourceID)
	} else {
		d.app.RequestUpdate()
	}
}
