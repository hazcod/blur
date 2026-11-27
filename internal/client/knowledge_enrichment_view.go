package client

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/knowledge/enrich"
	"github.com/hazcod/blur/internal/platform/darwin"
)

func libraryStatusCopy(status enrich.Status, provider ...string) string {
	if status.CoverageLimited && status.State != "disabled" {
		switch status.LastErrorCode {
		case "model_refused":
			return "The current local model declined some sources. Their saved text remains available in Ask. Choose a different local model to process those sources; other sources continue to build Library."
		case "invalid_output":
			return "Some sources returned unusable results. Their saved text remains available in Ask. Retry the unusable results or choose another local model; other sources continue to build Library."
		}
		return "Some sources could not be processed. Their saved text remains available in Ask; other sources continue to build Library. Check saved sources to retry."
	}
	switch status.State {
	case "disabled":
		return "Automatic Library is off. You can still add entries yourself."
	case "ready":
		return "Saved sources have been processed. Inferred memories remain yours to check and correct."
	case "indexing":
		return "Building Library from saved sources in the background."
	case "paused":
		return "Library updates are paused by your power settings. Existing memories remain available."
	}
	switch status.ErrorCode {
	case "local_model_required":
		return "Select Apple’s on-device model or an installed local Ollama model. Cloud models cannot build Library."
	case "model_missing":
		if len(provider) > 0 && provider[0] == ProviderApple {
			return "Apple’s on-device model is not ready. Check Apple Intelligence availability in AI settings, then retry."
		}
		return "The local answer model is not installed. Install it in Ollama, then retry."
	case "model_changed":
		return "The local model changed during processing. Retry to rebuild with the current model."
	case "capacity":
		return "Library processing reached a limit. Coverage is incomplete; your saved sources remain available in Search."
	default:
		return "Library updates are unavailable. Check the saved local answer model in Settings, then retry."
	}
}

func libraryRetryAction(status enrich.Status) (string, bool) {
	if status.LastErrorCode == "model_refused" {
		return "Check for new sources", false
	}
	return "Retry saved sources", true
}

func (d *desktop) librarySettingsView() ui.View {
	rows := []ui.View{wrapLabel("Automatic Library").Style(styleSectionTitle)}
	if d.client == nil || d.client.localAI == nil {
		return surfaceBox(14, 8, append(rows, wrapLabel("Library processing is unavailable on this build."))...)
	}
	saved := d.client.LocalAISettings()
	status := d.client.LibraryStatus()
	rows = append(rows, wrapLabel(libraryStatusCopy(status, effectiveProvider(d.client.LocalAISettings()))).Style(modern.MutedText), wrapLabel("Build people, projects, topics, places, and workflows from your saved captures and notes. Captured text goes only to the saved local answer model. Memories and their sources stay encrypted. Entries from different apps or sites are kept separate.").Style(modern.MutedText))
	if saved.AutomaticLibrary {
		action, retry := libraryRetryAction(status)
		rows = append(rows, flatButton(action).Enabled(!d.loading).OnClick(func() {
			if retry {
				d.client.localAI.RetryLibrary()
			} else {
				d.client.localAI.WakeLibrary()
			}
		}), flatButton("Pause automatic Library").Enabled(!d.loading).OnClick(func() { d.configureAutomaticLibrary(false) }))
	} else {
		ready := saved.Enabled && isLocalCompletionProvider(effectiveProvider(saved)) && !d.aiNeedsRepair
		if !ready {
			rows = append(rows, wrapLabel("Enable a local answer model in AI settings first.").Style(modern.MutedText))
		}
		rows = append(rows, primaryButton("Enable automatic Library").Enabled(ready && !d.loading).OnClick(func() { d.configureAutomaticLibrary(true) }))
	}
	return surfaceBox(14, 8, hugButtons(rows)...)
}

func (d *desktop) configureAutomaticLibrary(enabled bool) {
	if d == nil || d.client == nil || d.loading {
		return
	}
	d.loading = true
	d.app.RequestUpdate()
	go func() {
		err := d.client.SetAutomaticLibrary(context.Background(), enabled)
		d.app.Post(func() {
			d.loading = false
			if err != nil {
				d.status = "Library preference could not be saved. Check your local model settings."
			} else {
				d.status = libraryStatusCopy(d.client.LibraryStatus(), effectiveProvider(d.client.LocalAISettings()))
			}
			d.app.RequestUpdate()
		})
	}()
}

func inferredContext(entity knowledge.Entity) string {
	if entity.Inference == nil {
		if enrich.IsEntityID(entity.ID) {
			return "Includes your corrections"
		}
		return "Saved by you"
	}
	if entity.Inference.AppID == "sh.blur.meetings" {
		text := "Inferred from voice notes"
		if at := darwin.LocalizedDateTime(entity.Inference.SourceTime, localZoneName()); at != "" {
			text += " · " + at
		}
		return text
	}
	context := "saved sources"
	if entity.Inference.AppID != "" {
		context = displayAppName(entity.Inference.AppID)
	}
	if entity.Inference.Domain != "" {
		context += " / " + entity.Inference.Domain
	}
	return "Inferred from " + context
}

func libraryContextAction(entity knowledge.Entity) string {
	if entity.Inference == nil {
		if enrich.IsEntityID(entity.ID) {
			return "Review your corrections"
		}
		return "Review your saved details"
	}
	if entity.Inference.AppID == "sh.blur.meetings" {
		return "Review voice note"
	}
	context := "saved sources"
	if entity.Inference.AppID != "" {
		context = displayAppName(entity.Inference.AppID)
	}
	if entity.Inference.Domain != "" {
		context += " · " + entity.Inference.Domain
	}
	return "Review " + context
}

func librarySourceContext(source SearchHit) string {
	if source.AppID == "sh.blur.meetings" {
		text := "Voice note"
		if !source.TimeUnknown {
			if at := darwin.LocalizedDateTime(source.CapturedAt, localZoneName()); at != "" {
				text += " · " + at
			}
		}
		return text
	}
	return displayAppName(source.AppID) + " / " + source.Domain
}

func (d *desktop) inferredEntityCard(entity knowledge.Entity) ui.View {
	rows := []ui.View{wrapLabel(entity.Name).Style(styleSectionTitle), wrapLabel(string(entity.Kind) + " · " + inferredContext(entity)).Style(styleMeta)}
	rows = append(rows, d.librarySubjectAccount(entity, false)...)
	rows = append(rows, flatButton("Read subject and sources").Enabled(!d.libraryInspectLoading && !d.loading).OnClick(func() { d.inspectLibraryEntity(entity.ID, 0) }))
	return surfaceBox(14, 10, hugButtons(rows)...)
}

// Names that differ only by case, whitespace, hyphens, or underscores are
// shown as one subject row while retaining separate, inspectable records.
// This only groups the presentation; it does not merge or assert that the
// underlying identities are identical.
func (d *desktop) librarySubjectGroupCard(entities []knowledge.Entity) ui.View {
	if len(entities) == 0 {
		return ui.VBox()
	}
	first := entities[0]
	kindSet := map[string]struct{}{}
	for _, entity := range entities {
		kindSet[string(entity.Kind)] = struct{}{}
	}
	kinds := make([]string, 0, len(kindSet))
	for kind := range kindSet {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	rows := []ui.View{
		wrapLabel(first.Name).Style(styleSectionTitle),
		wrapLabel(strings.Join(kinds, " · ") + " · " + fmt.Sprintf("%d saved contexts", len(entities))).Style(styleMeta),
	}
	shownFacts := 0
	type groupFact struct {
		fact     knowledge.Fact
		contexts []string
	}
	facts := make([]groupFact, 0)
	stateRank := map[knowledge.State]int{knowledge.Confirmed: 0, knowledge.Deterministic: 1, knowledge.Proposed: 2}
	for _, entity := range entities {
		for _, fact := range entity.Facts {
			if strings.TrimSpace(fact.Text) == "" || fact.State == knowledge.Dismissed {
				continue
			}
			if _, visible := stateRank[fact.State]; !visible {
				continue
			}
			facts = append(facts, groupFact{fact: fact, contexts: []string{inferredContext(entity)}})
		}
	}
	sort.SliceStable(facts, func(i, j int) bool {
		if stateRank[facts[i].fact.State] != stateRank[facts[j].fact.State] {
			return stateRank[facts[i].fact.State] < stateRank[facts[j].fact.State]
		}
		return libraryDetailKey(facts[i].fact.Text) < libraryDetailKey(facts[j].fact.Text)
	})
	factIndex := map[string]int{}
	groupedFacts := make([]groupFact, 0, len(facts))
	for _, item := range facts {
		key := string(item.fact.State) + ":" + libraryDetailKey(item.fact.Text)
		if index, exists := factIndex[key]; exists {
			groupedFacts[index].contexts = appendUniqueLibraryContext(groupedFacts[index].contexts, item.contexts...)
			continue
		}
		factIndex[key] = len(groupedFacts)
		groupedFacts = append(groupedFacts, item)
	}
	facts = groupedFacts
	for _, item := range facts {
		if libraryDetailKey(item.fact.Text) == "" {
			continue
		}
		if shownFacts >= 4 {
			continue
		}
		text := item.fact.Text
		if item.fact.State == knowledge.Proposed {
			text += " (inferred, not confirmed)"
		}
		rows = append(rows, ui.VBox(wrapLabel(text), wrapLabel(strings.Join(item.contexts, " · ")).Style(styleMeta)).Spacing(3))
		shownFacts++
	}
	type groupLoop struct {
		loop     knowledge.OpenLoop
		contexts []string
	}
	loops := make([]groupLoop, 0)
	for _, entity := range entities {
		for _, loop := range entity.OpenLoops {
			key := libraryDetailKey(loop.Text)
			if key == "" || loop.State == knowledge.Dismissed {
				continue
			}
			if loop.State != knowledge.Confirmed && loop.State != knowledge.Deterministic && loop.State != knowledge.Proposed {
				continue
			}
			loops = append(loops, groupLoop{loop: loop, contexts: []string{inferredContext(entity)}})
		}
	}
	sort.SliceStable(loops, func(i, j int) bool {
		if loops[i].loop.State != loops[j].loop.State {
			return stateRank[loops[i].loop.State] < stateRank[loops[j].loop.State]
		}
		return libraryDetailKey(loops[i].loop.Text) < libraryDetailKey(loops[j].loop.Text)
	})
	loopIndex := map[string]int{}
	uniqueLoops := loops[:0]
	for _, item := range loops {
		key := string(item.loop.State) + ":" + libraryDetailKey(item.loop.Text)
		if index, exists := loopIndex[key]; exists {
			uniqueLoops[index].contexts = appendUniqueLibraryContext(uniqueLoops[index].contexts, item.contexts...)
			continue
		}
		loopIndex[key] = len(uniqueLoops)
		uniqueLoops = append(uniqueLoops, item)
	}
	loops = uniqueLoops
	shownLoops := min(len(loops), 3)
	if shownLoops > 0 {
		rows = append(rows, recapSectionTitle("Open questions"))
		for _, item := range loops[:shownLoops] {
			text := item.loop.Text
			if item.loop.State == knowledge.Proposed {
				text += " (inferred, not confirmed)"
			}
			rows = append(rows, ui.VBox(wrapLabel(text), wrapLabel(strings.Join(item.contexts, " · ")).Style(styleMeta)).Spacing(3))
		}
	}
	if shownFacts == 0 && shownLoops == 0 {
		rows = append(rows, wrapLabel("No learned details yet.").Style(modern.MutedText))
	}
	seen := map[string]int{}
	for _, entity := range entities {
		label := libraryContextAction(entity)
		seen[label]++
		if seen[label] > 1 {
			label = fmt.Sprintf("%s (%d)", label, seen[label])
		}
		id := entity.ID
		rows = append(rows, flatButton(label).Enabled(!d.libraryInspectLoading && !d.loading).OnClick(func() { d.inspectLibraryEntity(id, 0) }))
	}
	return surfaceBox(14, 10, hugButtons(rows)...)
}

func appendUniqueLibraryContext(contexts []string, added ...string) []string {
	seen := make(map[string]bool, len(contexts)+len(added))
	for _, context := range contexts {
		seen[context] = true
	}
	for _, context := range added {
		context = strings.TrimSpace(context)
		if context != "" && !seen[context] {
			contexts = append(contexts, context)
			seen[context] = true
		}
	}
	return contexts
}

func (d *desktop) inspectedLibraryView() ui.View {
	entity := d.libraryInspect
	if entity == nil {
		return ui.VBox()
	}
	rows := []ui.View{flatButton("Back to Library").OnClick(func() {
		d.libraryInspectRequest++
		d.libraryInspectLoading = false
		d.libraryInspect = nil
		d.librarySource = nil
		d.libraryEditFact = ""
		if d.libraryRefreshPending {
			d.refreshLibrary()
		}
		d.app.RequestUpdate()
	}), wrapLabel(entity.Name).Style(styleSectionTitle), wrapLabel(inferredContext(*entity)).Style(styleMeta)}
	rows = append(rows, d.librarySubjectAccount(*entity, true)...)
	if entity.FactsHasMore {
		rows = append(rows, flatButton("Load more learned details").Enabled(!d.libraryInspectLoading && !d.loading).OnClick(func() { d.loadMoreInferredFacts(entity.ID) }))
	}
	if len(entity.ActivitySourceIDs) > 0 {
		rows = append(rows, recapSectionTitle("Original sources"))
		for i, sourceID := range entity.ActivitySourceIDs {
			id := sourceID
			rows = append(rows, flatButton(fmt.Sprintf("Inspect source %d", i+1)).Enabled(!d.libraryInspectLoading).OnClick(func() { d.inspectLibrarySource(id) }))
		}
	}
	if d.libraryInspectLoading {
		rows = append(rows, wrapLabel("Loading saved evidence…").Style(modern.MutedText))
	}
	if d.librarySource != nil {
		source := d.librarySource
		text, truncated := boundedPreview(source.Text, 5000)
		if d.librarySourceExpanded {
			text = source.Text
		}
		rows = append(rows, recapSectionTitle("Captured source"), wrapLabel(librarySourceContext(*source)).Style(styleMeta), wrapLabel(text))
		if truncated {
			label := "Show complete source"
			if d.librarySourceExpanded {
				label = "Show less"
			}
			rows = append(rows, flatButton(label).OnClick(func() { d.librarySourceExpanded = !d.librarySourceExpanded; d.app.RequestUpdate() }))
		}
		rows = append(rows, flatButton("Close source").OnClick(func() { d.librarySource = nil; d.app.RequestUpdate() }))
	}
	if len(entity.RelatedIDs) > 0 {
		rows = append(rows, recapSectionTitle("Mentioned in the same sources"), wrapLabel("These connections do not confirm that similarly named people are the same person.").Style(modern.MutedText))
		for _, related := range d.libraryRelated {
			id := related.ID
			rows = append(rows, flatButton(related.Name+" ("+string(related.Kind)+")").Enabled(!d.libraryInspectLoading).OnClick(func() { d.inspectLibraryEntity(id, 0) }))
		}
		if d.libraryRelatedOffset+len(d.libraryRelated) < len(entity.RelatedIDs) {
			rows = append(rows, flatButton("More connections").Enabled(!d.libraryInspectLoading).OnClick(func() { d.inspectLibraryEntity(entity.ID, d.libraryRelatedOffset+len(d.libraryRelated)) }))
		}
		if d.libraryRelatedOffset > 0 {
			rows = append(rows, flatButton("First connections").Enabled(!d.libraryInspectLoading).OnClick(func() { d.inspectLibraryEntity(entity.ID, 0) }))
		}
	}
	if !enrich.IsEntityID(entity.ID) {
		return surfaceBox(14, 10, hugButtons(rows)...)
	}
	if d.pendingDeleteKnowledge == entity.ID {
		rows = append(rows, wrapLabel("Remove this entry and its corrections? Captured sources remain saved. Automatic processing of these sources will not restore it.").Style(modern.MutedText), dangerButton("Remove entry permanently").Enabled(!d.loading).OnClick(func() { d.removeInferredEntry(entity.ID) }), flatButton("Cancel removal").OnClick(func() { d.pendingDeleteKnowledge = ""; d.app.RequestUpdate() }))
	} else {
		rows = append(rows, flatButton("Remove entry").Enabled(!d.loading).OnClick(func() { d.pendingDeleteKnowledge = entity.ID; d.app.RequestUpdate() }))
	}
	return surfaceBox(14, 8, hugButtons(rows)...)
}

func (d *desktop) loadMoreInferredFacts(id string) {
	entity := d.libraryInspect
	if d.client == nil || entity == nil || entity.ID != id || !entity.FactsHasMore || entity.FactsNext == "" || d.libraryInspectLoading {
		return
	}
	d.libraryInspectLoading = true
	d.libraryInspectRequest++
	request, after := d.libraryInspectRequest, entity.FactsNext
	d.app.RequestUpdate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		page, err := d.client.InferredFactPage(ctx, id, after, knowledge.MaxPageSize)
		d.app.Post(func() {
			if request != d.libraryInspectRequest {
				return
			}
			d.libraryInspectLoading = false
			if err != nil || d.libraryInspect == nil || d.libraryInspect.ID != id {
				d.status = "More Library details could not be loaded. Refresh the entry and retry."
			} else {
				d.libraryInspect.Facts = append(d.libraryInspect.Facts, page.Facts...)
				d.libraryInspect.FactsNext = page.Next
				d.libraryInspect.FactsHasMore = page.HasMore
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) inspectLibraryEntity(id string, offset int) {
	if d.client == nil || d.libraryInspectLoading {
		return
	}
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		return
	}
	d.libraryInspectLoading = true
	d.libraryInspectRequest++
	request := d.libraryInspectRequest
	d.app.RequestUpdate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		entity, err := d.client.InferredKnowledge(ctx, id)
		related := []knowledge.Entity{}
		if err == nil && offset >= 0 && offset < len(entity.RelatedIDs) {
			for _, otherID := range entity.RelatedIDs[offset:min(offset+16, len(entity.RelatedIDs))] {
				other, readErr := d.client.InferredKnowledge(ctx, otherID)
				if readErr != nil {
					err = readErr
					break
				}
				related = append(related, other)
			}
		}
		d.app.Post(func() {
			if request != d.libraryInspectRequest {
				return
			}
			if !d.client.PublishMemoryRead(epoch, func() {
				d.libraryInspectLoading = false
				if err != nil {
					d.status = "This Library entry could not be read. Refresh Library and retry."
					return
				}
				d.libraryInspect = &entity
				d.libraryRelated = related
				d.libraryRelatedOffset = offset
				d.librarySource = nil
				d.libraryEditFact = ""
			}) {
				return
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) inspectLibrarySource(id string) {
	if d.client == nil || d.libraryInspectLoading {
		return
	}
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		return
	}
	d.libraryInspectLoading = true
	d.libraryInspectRequest++
	request := d.libraryInspectRequest
	d.app.RequestUpdate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		source, err := d.client.ChronicleSource(ctx, id)
		if err == nil {
			source.CapturedAt, source.TimeUnknown, _ = d.client.sourceCaptureTime(ctx, id, time.Time{})
			if ctx.Err() != nil {
				err = ctx.Err()
			}
		}
		d.app.Post(func() {
			if request != d.libraryInspectRequest {
				return
			}
			if !d.client.PublishMemoryRead(epoch, func() {
				d.libraryInspectLoading = false
				if err != nil {
					d.status = "This source is no longer available."
					return
				}
				d.librarySource = &source
				d.librarySourceExpanded = false
			}) {
				return
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) saveLibraryFact(id, factID string, dismiss bool) {
	if d.loading || d.client == nil {
		return
	}
	text := d.libraryEditText
	d.loading = true
	d.app.RequestUpdate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := d.client.ChangeInferredFact(ctx, id, factID, text, dismiss)
		d.app.Post(func() {
			d.loading = false
			if err != nil {
				d.status = "The memory could not be changed. Refresh Library and retry."
			} else {
				d.status = "Memory updated. Your change takes precedence during source re-indexing."
				d.libraryEditFact = ""
				d.libraryLoaded = false
				d.loadLibrary()
				d.inspectLibraryEntity(id, 0)
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) removeInferredEntry(id string) {
	if d.loading || d.client == nil {
		return
	}
	d.loading = true
	d.app.RequestUpdate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := d.client.DeleteKnowledge(ctx, id)
		d.app.Post(func() {
			d.loading = false
			if err != nil {
				d.status = "This entry could not be removed. Try again."
			} else {
				d.libraryInspect = nil
				d.librarySource = nil
				d.pendingDeleteKnowledge = ""
				d.libraryLoaded = false
				d.loadLibrary()
			}
			d.app.RequestUpdate()
		})
	}()
}
