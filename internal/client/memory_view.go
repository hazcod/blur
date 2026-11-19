package client

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/golang-gui/goui/core/geometry"
	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/platform/graphics"
	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
	"github.com/hazcod/blur/internal/meetings"
)

type memoryViewState struct {
	recap              voiceRecapViewState
	keyboardWidget     gui.Widget
	keyboardController *gui.KeyEventController
	voiceSourceRequest uint64
	page               memoryBrowsePage
	after              memoryBrowseCursor
	previous           []memoryBrowseCursor
	loaded, loading    bool
	request            uint64
	status             string
	meetingDetail      bool
	entry              EntryState
	entryRequest       uint64
	entryCancel        context.CancelFunc
	entryLoading       bool
	askMode            string
	submittedInput     string
	answerInput        string
	taskSubmission     *taskSubmission
	copyText           func(string) error
	searchInput        string
	revealedSource     *SearchHit
	notesExporting     bool
	notesExportRequest uint64
	notesExportCancel  context.CancelFunc
	chooseNotesExport  func(string) (string, error)
}

func (d *desktop) loadMemoryBrowse(after memoryBrowseCursor) {
	if d == nil || d.client == nil || d.client.data == nil || d.memoryUI.loading {
		return
	}
	epoch, stable := d.client.BeginMemoryRead()
	if !stable {
		return
	}
	u := &d.memoryUI
	u.loading, u.status = true, ""
	u.request++
	request, generation := u.request, d.memoryGeneration
	d.app.RequestUpdate()
	go func() {
		page, err := d.client.browseSavedMemory(context.Background(), after)
		d.app.Post(func() {
			if request != u.request || generation != d.memoryGeneration {
				return
			}
			u.loading = false
			if !d.client.PublishMemoryRead(epoch, func() {
				if err != nil {
					u.status = friendlyError(err)
				} else {
					u.page, u.after, u.loaded = page, after, true
					if u.revealedSource != nil {
						for _, item := range page.items {
							if item.SourceID == u.revealedSource.SourceID {
								u.revealedSource = nil
								break
							}
						}
					}
				}
			}) {
				return
			}
			d.app.RequestUpdate()
		})
	}()
}

func (d *desktop) memoryResultViews() []ui.View {
	u := &d.memoryUI
	if u.meetingDetail {
		rows := d.voiceRecapViews()
		if d.meetingUI.status != "" {
			rows = append(rows, wrapLabel(d.meetingUI.status).Style(styleMeta))
		}
		return append(rows, d.meetingTranscriptView())
	}
	if d.askResult != nil || d.askLoading {
		return nil
	}
	if strings.TrimSpace(d.askQuestion) != "" {
		var rows []ui.View
		if len(d.searchHits) > 0 {
			rows = append(rows, wrapLabel("Matching sources").Style(styleSectionTitle))
		}
		closest := false
		for _, hit := range d.searchHits {
			if hit.Relaxed && !closest {
				closest = true
				rows = append(rows, searchDivider())
			} else {
				rows = append(rows, hairline())
			}
			rows = append(rows, d.memorySourceRow(hit))
		}
		if len(d.searchSummaries) > 0 {
			rows = append(rows, hairline(), wrapLabel("Summaries").Style(styleMeta))
			for _, summary := range d.searchSummaries {
				rows = append(rows, summaryRow(summary, d.searchTerms), hairline())
			}
			rows = rows[:len(rows)-1]
		}
		if d.searchComplete && !d.searchFailed && len(d.searchHits) == 0 && len(d.searchSummaries) == 0 {
			rows = append(rows, wrapLabel("No matching saved memory found. Try another topic or a question.").Style(modern.MutedText))
		}
		if d.searchIndexingPending && d.searchComplete {
			rows = append(rows, wrapLabel(pendingExactIndexCopy).Style(styleMeta))
		}
		if d.searchCoverageLimited && d.searchComplete {
			rows = append(rows, wrapLabel("Some matches may be missing from the memory that could be searched.").Style(styleMeta))
		}
		return rows
	}
	heading := []ui.View{wrapLabel("Saved memory").Style(styleSectionTitle).MainWeight(1)}
	if supportsMarkdownArchiveExport() || u.chooseNotesExport != nil {
		heading = append(heading, flatButton("Export notes").Enabled(!u.notesExporting && !u.loading && d.client != nil).OnClick(d.exportSavedMarkdownNotes))
	}
	rows := []ui.View{ui.HBox(heading...).Spacing(10).CrossAlign(layout.CrossCenter)}
	if u.notesExporting {
		rows = append(rows, ui.HBox(
			wrapLabel("Exporting your Markdown notes…").Style(styleMeta).MainWeight(1),
			flatButton("Cancel export").OnClick(func() {
				if u.notesExportCancel != nil {
					u.notesExportCancel()
					u.status = "Canceling export…"
					d.app.RequestUpdate()
				}
			}),
		).Spacing(8).CrossAlign(layout.CrossCenter))
	}
	if u.revealedSource != nil {
		rows = append(rows, hairline(), d.memorySourceRow(*u.revealedSource))
	}
	if u.status != "" {
		rows = append(rows, wrapLabel(u.status).Style(styleMeta), flatButton("Try again").Enabled(!u.loading).OnClick(func() { d.loadMemoryBrowse(u.after) }))
	}
	if u.loading {
		rows = append(rows, wrapLabel("Loading saved memory…").Style(styleMeta))
	}
	if len(u.page.items) == 0 && u.revealedSource == nil && u.loaded && !u.loading {
		rows = append(rows, wrapLabel("No saved items on this page. Notes, voice notes, and captured sources appear here.").Style(modern.MutedText))
	}
	for _, item := range u.page.items {
		if u.revealedSource != nil && item.SourceID != "" && item.SourceID == u.revealedSource.SourceID {
			continue
		}
		if item.meeting != nil {
			meeting, generation := *item.meeting, d.memoryGeneration
			rows = append(rows, hairline(), ui.VBox(
				d.memorySourceHeading(item.SearchHit, "Voice note, "+meeting.Title),
				wrapLabel(meeting.StartedAt.In(time.Local).Format("2 Jan 2006, 15:04")+fmt.Sprintf(", %d saved passages", meeting.RecordCount)).Style(styleMeta),
				hug(flatButton("Read voice note").Enabled(!d.meetingUI.busy).OnClick(func() {
					if generation == d.memoryGeneration {
						d.openMemoryMeeting(meeting)
					}
				})),
			).Spacing(6).CrossAlign(layout.CrossStretch))
		} else {
			rows = append(rows, hairline(), d.memorySourceRow(item.SearchHit))
		}
	}
	if u.page.notesCatchingUp {
		rows = append(rows, wrapLabel("Older notes are being prepared for browsing.").Style(styleMeta), flatButton("Refresh saved memory").Enabled(!u.loading).OnClick(func() { u.previous = nil; d.loadMemoryBrowse(memoryBrowseCursor{}) }))
	}
	if u.page.pending {
		rows = append(rows, wrapLabel(pendingExactIndexCopy).Style(styleMeta))
	}
	if len(u.previous) > 0 || u.page.more {
		rows = append(rows, ui.HBox(
			flatButton("Previous saved items").Enabled(!u.loading && len(u.previous) > 0).OnClick(func() {
				after := u.previous[len(u.previous)-1]
				u.previous = u.previous[:len(u.previous)-1]
				d.loadMemoryBrowse(after)
			}),
			flatButton("Next saved items").Enabled(!u.loading && u.page.more && (!u.page.pending || u.page.next != u.after)).OnClick(func() {
				u.revealedSource = nil
				u.previous = append(u.previous, u.after)
				d.loadMemoryBrowse(u.page.next)
			}),
		).Spacing(8))
	}
	return rows
}

func (d *desktop) openMemoryMeeting(meeting meetings.Meeting) {
	epoch, stable := d.client.BeginMemoryRead()
	if !stable || !d.client.PublishMemoryRead(epoch, func() { d.selectMemoryMeeting(meeting) }) {
		return
	}
	d.loadMeetingTranscript(0)
	d.app.RequestUpdate()
}

func (d *desktop) selectMemoryMeeting(meeting meetings.Meeting) {
	if d.memoryUI.recap.id != "" && d.memoryUI.recap.id != meeting.ID {
		resumeEditor := d.memoryUI.recap.resumeEditor
		d.invalidateVoiceRecap()
		d.memoryUI.recap.resumeEditor = resumeEditor
	}
	u := &d.meetingUI
	u.selected, u.selectedTitle, u.selectedAt, u.selectedState = meeting.ID, meeting.Title, meeting.StartedAt, meeting.State
	u.selectedRecordCount, u.transcriptExpanded = meeting.RecordCount, false
	u.browseSaved, u.transcriptPrev, u.status = true, nil, ""
	d.memoryUI.meetingDetail = true
}

func (d *desktop) memorySourceHeading(hit SearchHit, title string) ui.View {
	return ui.HBox(memorySourceIcon(sourceKind(hit), d.tokens), wrapLabel(title).Style(styleMeta).MainWeight(1)).Spacing(8).CrossAlign(layout.CrossCenter)
}

func (d *desktop) memorySourceRow(hit SearchHit) ui.View {
	if hit.Task != nil {
		return d.taskCard(*hit.Task)
	}
	if usesSearchPreview(hit) {
		return d.searchPreviewRow(hit)
	}
	previewText := hit.Text
	caption := sourceKind(hit)
	noteHeading := false
	if hit.AppID == "sh.blur.notes" {
		if title, body, found := markdownNoteHeading(hit.Text); found {
			noteHeading = true
			caption = "Note · " + title
			previewText = body
			if strings.TrimSpace(previewText) == "" {
				previewText = title
			}
		}
	}
	if !noteHeading && hit.MatchEnd > hit.MatchStart && hit.MatchStart >= 0 && hit.MatchEnd <= len(hit.Text) {
		previewText = hit.Text[hit.MatchStart:hit.MatchEnd]
	}
	preview, truncated := searchPreview(previewText)
	truncated = truncated || previewText != hit.Text
	expanded := d.expandedSourceID == hit.SourceID
	if expanded {
		preview = hit.Text
	}
	if hit.Domain != "" && hit.AppID != "sh.blur.meetings" && hit.AppID != "sh.blur.notes" {
		caption += ", " + hit.Domain
	}
	if hit.AppID != "" && hit.AppID != "sh.blur.meetings" && hit.AppID != "sh.blur.notes" {
		caption += ", " + displayAppName(hit.AppID)
	}
	if hit.TimeUnknown {
		caption += ", Capture time unavailable"
	} else if !hit.CapturedAt.IsZero() {
		caption += ", " + hit.CapturedAt.In(time.Local).Format("2 Jan 2006, 15:04")
	}
	rows := []ui.View{d.memorySourceHeading(hit, caption), wrapLabel(preview)}
	if hit.AppID == "sh.blur.notes" {
		rows = append(rows, hug(flatButton("Edit note").Enabled(!d.loading && !d.askLoading).OnClick(func() { d.openNoteDocument(hit.SourceID) })))
	}
	if action := d.memoryVoiceSourceAction(hit.SourceID, hit.AppID); action != nil {
		rows = append(rows, action)
	}
	if hit.Partial {
		rows = append(rows, wrapLabel("Only part of the original content was captured.").Style(styleMeta))
	}
	if truncated {
		label := "Read full source"
		if expanded {
			label = "Show preview"
		}
		rows = append(rows, hug(flatButton(label).OnClick(func() {
			if d.expandedSourceID == hit.SourceID {
				d.expandedSourceID = ""
			} else {
				d.expandedSourceID = hit.SourceID
			}
			d.app.RequestUpdate()
		})))
	}
	return d.withSourceActions(hit, rows)
}

// withSourceActions appends the delete controls every source row carries.
func (d *desktop) withSourceActions(hit SearchHit, rows []ui.View) ui.View {
	if d.client != nil && d.client.CanDeleteSource() {
		id := hit.SourceID
		if d.pendingDelete == id {
			rows = append(rows, wrapLabel("Delete this source and its derived recap items? This cannot be undone.").Style(styleMeta), ui.HBox(
				flatButton("Cancel").Enabled(!d.loading).OnClick(func() { d.pendingDelete = ""; d.app.RequestUpdate() }),
				dangerButton("Delete permanently").Enabled(!d.loading).OnClick(func() { d.deleteSource(id) }),
			).Spacing(8))
		} else {
			rows = append(rows, hug(flatButton("Delete from memory").Enabled(!d.loading).OnClick(func() { d.pendingDelete = id; d.app.RequestUpdate() })))
		}
	}
	return ui.VBox(rows...).Spacing(6).CrossAlign(layout.CrossStretch)
}

// Native, monochrome source markers share one stroke and size. Text labels
// alongside them carry the meaning without depending on the drawing.
func memorySourceIcon(kind string, tokens palette) ui.View {
	return ui.DrawingArea(func(p gui.Painter, _ geometry.Size) {
		ink := graphics.ColorOf(tokens.inkMuted)
		if kind == "Voice note" {
			p.DrawRoundRect(geometry.Rect(6, 1, 6, 10), 3, 1.4, ink)
			p.DrawPath(graphics.MoveTo(3, 8).LineTo(3, 10).ArcTo(6, 5, 0, 0, 0, 15, 10).LineTo(15, 8), 1.4, ink)
			p.DrawLine(geometry.Point{X: 9, Y: 15}, geometry.Point{X: 9, Y: 18}, 1.4, ink)
			p.DrawLine(geometry.Point{X: 6, Y: 18}, geometry.Point{X: 12, Y: 18}, 1.4, ink)
		} else {
			p.DrawRoundRect(geometry.Rect(3, 2, 12, 15), 2, 1.4, ink)
			p.DrawLine(geometry.Point{X: 6, Y: 7}, geometry.Point{X: 12, Y: 7}, 1.2, ink)
			p.DrawLine(geometry.Point{X: 6, Y: 11}, geometry.Point{X: 11, Y: 11}, 1.2, ink)
		}
	}).MinSize(18, 20).MaxSize(18, 20)
}

// searchPreviewRow is a capture search row: "App · Title · date time", the
// preview with its matched words emphasised, and the full capture one click
// away. The full text stays the source of truth for the detail view.
func (d *desktop) searchPreviewRow(hit SearchHit) ui.View {
	expanded := d.expandedSourceID == hit.SourceID
	var body ui.View
	if expanded {
		body = wrapLabel(hit.Text)
	} else {
		body = excerptView(hit.Excerpt, hit.MatchedTerms)
	}
	rows := []ui.View{d.memorySourceHeading(hit, searchHitCaption(hit)), body}
	if hit.Partial {
		rows = append(rows, wrapLabel("Only part of the original content was captured.").Style(styleMeta))
	}
	if strings.Join(strings.Fields(hit.Text), " ") != strings.Join(strings.Fields(hit.Excerpt), " ") {
		label := "Read full source"
		if expanded {
			label = "Show preview"
		}
		id := hit.SourceID
		rows = append(rows, hug(flatButton(label).OnClick(func() {
			if d.expandedSourceID == id {
				d.expandedSourceID = ""
			} else {
				d.expandedSourceID = id
			}
			d.app.RequestUpdate()
		})))
	}
	return d.withSourceActions(hit, rows)
}
