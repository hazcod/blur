package client

import (
	"context"
	"time"

	"github.com/hazcod/blur/internal/meetings"
)

func (c *Client) configureMeetings() {
	repo, ok := c.data.(meetings.Repository)
	if !ok {
		return
	}
	m := newMeetingController(repo, openMeetingAudio)
	m.writable = func() bool { return !c.MemoryIsChanging() }
	m.onSource = func(id string) {
		c.exactIndex.Wake()
		c.memoryIndex.NotifySource(id)
		c.localAI.NotifyLibrarySource(id)
		c.NotifyCaptureChanged()
	}
	c.meetings = m
	c.addMemoryChangingHook(func() { m.invalidate(false) })
}

func (c *Client) CanUseMeetings() bool { return c != nil && c.meetings != nil }

func (c *Client) MeetingLive() MeetingLiveView {
	if c == nil {
		return MeetingLiveView{Issue: "storage_unavailable"}
	}
	return c.meetings.snapshot()
}

func (c *Client) MeetingChanges() <-chan struct{} {
	if !c.CanUseMeetings() {
		return nil
	}
	return c.meetings.changes
}

func (c *Client) ProbeMeetings(ctx context.Context, locale string) error {
	if !c.CanUseMeetings() {
		return ErrNotConfigured
	}
	op, done, err := c.beginOperation(ctx, 15*time.Second)
	if err != nil {
		return err
	}
	defer done()
	return c.meetings.probe(op, locale)
}

// StartMeeting is an explicit action, including any OS permission prompt. It
// never downloads speech assets: InstallMeetingLanguage is separate consent.
func (c *Client) StartMeeting(ctx context.Context, title, locale string, tracks []string) error {
	if !c.CanUseMeetings() {
		return ErrNotConfigured
	}
	op, done, err := c.beginOperation(ctx, 5*time.Minute)
	if err != nil {
		return err
	}
	defer done()
	return c.meetings.start(op, title, locale, tracks)
}

// StartMeetingNotes explicitly starts an existing saved Ready agenda using its
// saved title, language and tracks. Reading or selecting an agenda never calls
// this method. OS permissions may be requested; speech assets are not installed.
func (c *Client) StartMeetingNotes(ctx context.Context, id string) error {
	if !c.CanUseMeetings() {
		return ErrNotConfigured
	}
	op, done, err := c.beginOperation(ctx, 5*time.Minute)
	if err != nil {
		return err
	}
	defer done()
	return c.meetings.start(op, "", "", nil, id)
}

func (c *Client) InstallMeetingLanguage(ctx context.Context, locale string) error {
	if !c.CanUseMeetings() {
		return ErrNotConfigured
	}
	op, done, err := c.beginOperation(ctx, 5*time.Minute)
	if err != nil {
		return err
	}
	defer done()
	return c.meetings.installLocale(op, locale)
}

func (c *Client) ControlMeeting(ctx context.Context, operation string) error {
	if !c.CanUseMeetings() {
		return ErrNotConfigured
	}
	op, done, err := c.beginOperation(ctx, shortOperationTimeout)
	if err != nil {
		return err
	}
	defer done()
	return c.meetings.control(op, operation)
}

func (c *Client) AddMeetingNote(ctx context.Context, meeting string, note meetings.ManualNoteInput) error {
	if !c.CanUseMeetings() {
		return ErrNotConfigured
	}
	op, done, err := c.beginOperation(ctx, shortOperationTimeout)
	if err != nil {
		return err
	}
	defer done()
	return c.meetings.addNote(op, meeting, note)
}

func (c *Client) NewMeetingNotes(ctx context.Context, title, locale string, tracks []string) (string, error) {
	if !c.CanUseMeetings() {
		return "", ErrNotConfigured
	}
	op, done, err := c.beginOperation(ctx, shortOperationTimeout)
	if err != nil {
		return "", err
	}
	defer done()
	return c.meetings.newNotes(op, title, locale, tracks)
}

func (c *Client) MeetingPage(ctx context.Context, after uint64) (meetings.MeetingPage, error) {
	if !c.CanUseMeetings() || c.MemoryIsChanging() {
		return meetings.MeetingPage{}, ErrNotConfigured
	}
	op, done, err := c.beginOperation(ctx, shortOperationTimeout)
	if err != nil {
		return meetings.MeetingPage{}, err
	}
	defer done()
	return c.meetings.repo.ListMeetings(op, meetings.PageRequest{After: after, Limit: 12})
}

func (c *Client) MeetingTranscript(ctx context.Context, id string, after uint64) (meetings.RecordPage, error) {
	page, _, err := c.readMeetingTranscript(ctx, id, after)
	return page, err
}

func (c *Client) readMeetingTranscript(ctx context.Context, id string, after uint64) (meetings.RecordPage, memoryReadPublication, error) {
	if !c.CanUseMeetings() || c.MemoryIsChanging() {
		return meetings.RecordPage{}, memoryReadPublication{}, ErrNotConfigured
	}
	op, done, err := c.beginOperation(ctx, shortOperationTimeout)
	if err != nil {
		return meetings.RecordPage{}, memoryReadPublication{}, err
	}
	defer done()
	read, stable := c.beginVoicePublication()
	if !stable {
		return meetings.RecordPage{}, memoryReadPublication{}, ErrMemoryChanging
	}
	page, err := c.meetings.repo.MeetingRecords(op, id, meetings.PageRequest{After: after, Limit: 20, MaxBytes: 128 << 10})
	if err != nil {
		return meetings.RecordPage{}, memoryReadPublication{}, err
	}
	if err := op.Err(); err != nil {
		return meetings.RecordPage{}, memoryReadPublication{}, err
	}
	if !read.publish(c, func() {}) {
		return meetings.RecordPage{}, memoryReadPublication{}, ErrMemoryChanging
	}
	return page, read, nil
}

func (c *Client) DeleteMeeting(ctx context.Context, id string) error {
	if !c.CanUseMeetings() {
		return ErrNotConfigured
	}
	deleter, ok := c.deletion.(interface {
		DeleteMeeting(context.Context, string) error
	})
	if !ok {
		return ErrNotConfigured
	}
	c.NotifyMemoryChanging()
	defer c.NotifyMemoryStable()
	op, done, err := c.beginOperation(ctx, noteOperationTimeout)
	if err != nil {
		return err
	}
	defer done()
	return deleter.DeleteMeeting(op, id)
}
