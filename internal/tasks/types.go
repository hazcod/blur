// Package tasks defines explicit user tasks and their durable reminder intent.
// Captured prose and model suggestions cannot create active tasks on their own.
package tasks

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxTitleBytes  = 512
	MaxItemBytes   = 4096
	MaxPageSize    = 32
	MaxSourceTasks = 64
	MaxSources     = 8
	MaxHistory     = 24
)

var (
	ErrInvalid    = errors.New("tasks: invalid request")
	ErrConflict   = errors.New("tasks: item changed")
	ErrSourceGone = errors.New("tasks: source removed")
	ErrCapacity   = errors.New("tasks: capacity reached")
)

type State string

const (
	Open      State = "open"
	Completed State = "completed"
	Cancelled State = "cancelled"
)

type Recurrence string

const (
	Once   Recurrence = ""
	Daily  Recurrence = "daily"
	Weekly Recurrence = "weekly"
)

type Reminder struct {
	DueAt    time.Time    `json:"due_at"`
	Timezone string       `json:"timezone"`
	Repeat   Recurrence   `json:"repeat,omitempty"`
	Hour     int          `json:"hour,omitempty"`
	Minute   int          `json:"minute,omitempty"`
	Weekday  time.Weekday `json:"weekday,omitempty"`
}

type DeliveryState string

const (
	NeedsSync  DeliveryState = "needs_sync"
	Scheduling DeliveryState = "scheduling"
	Scheduled  DeliveryState = "scheduled"
	Delivered  DeliveryState = "delivered"
	Elapsed    DeliveryState = "elapsed"
	Blocked    DeliveryState = "blocked"
	Failed     DeliveryState = "failed"
)

type Delivery struct {
	State     DeliveryState `json:"state,omitempty"`
	At        time.Time     `json:"at,omitempty"`
	Attempted bool          `json:"attempted,omitempty"`
	NextAt    time.Time     `json:"next_at,omitempty"`
}

type Item struct {
	ID             string       `json:"id"`
	RequestHash    string       `json:"request_hash,omitempty"`
	Revision       uint64       `json:"revision"`
	Title          string       `json:"title"`
	State          State        `json:"state"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
	SourceID       string       `json:"source_id,omitempty"`
	SourceIDs      []string     `json:"source_ids,omitempty"`
	UserAuthored   bool         `json:"user_authored,omitempty"`
	Reminder       *Reminder    `json:"reminder,omitempty"`
	Delivery       Delivery     `json:"delivery,omitempty"`
	History        []Transition `json:"history,omitempty"`
	HistoryOmitted uint64       `json:"history_omitted,omitempty"`
}

type Transition struct {
	State    State     `json:"state"`
	At       time.Time `json:"at"`
	Revision uint64    `json:"revision"`
}

// RecordTransition records state changes only; changing a title or a native
// delivery receipt must never change the recorded completion date.
func RecordTransition(item *Item, previous State) {
	if item == nil || item.State == previous {
		return
	}
	item.History = append(item.History, Transition{State: item.State, At: item.UpdatedAt, Revision: item.Revision})
	if excess := len(item.History) - MaxHistory; excess > 0 {
		item.HistoryOmitted += uint64(excess)
		item.History = append([]Transition(nil), item.History[excess:]...)
	}
}

type PageRequest struct {
	After string
	Limit int
	State State
}
type Page struct {
	Items   []Item
	Next    string
	HasMore bool
}

type Repository interface {
	EncryptedAtRest() bool
	GetTask(context.Context, string) (Item, bool, error)
	PutTask(context.Context, Item, uint64) error
	SetTaskDelivery(context.Context, string, uint64, Delivery) error
	ListTasks(context.Context, PageRequest) (Page, error)
	TasksForSources(context.Context, []string) ([]Item, error)
	DeleteTask(context.Context, string, uint64) error
	DeleteAllTasks(context.Context) error
}

// TodayCandidate is the exact visible suggestion an explicit click accepted.
// Its text and evidence links are checked again in the creation transaction.
type TodayCandidate struct {
	Date      string
	ID        string
	Text      string
	SourceIDs []string
}

func (c TodayCandidate) Validate() error {
	at, err := time.Parse("2006-01-02", c.Date)
	if err != nil || at.Format("2006-01-02") != c.Date || c.ID == "" || len(c.ID) > 128 || !utf8.ValidString(c.ID) || strings.ContainsRune(c.ID, 0) || !ValidTitle(c.Text) || len(c.SourceIDs) == 0 {
		return ErrInvalid
	}
	if len(c.SourceIDs) > MaxSources {
		return ErrCapacity
	}
	for n, id := range c.SourceIDs {
		if !ValidSourceID(id) || n > 0 && id <= c.SourceIDs[n-1] {
			return ErrInvalid
		}
	}
	return nil
}

type TodayRepository interface {
	ConfirmTodayTask(context.Context, TodayCandidate, time.Time) (Item, error)
}

type TodayDismissRepository interface {
	DismissTodayTask(context.Context, TodayCandidate) error
}

func ValidID(id string) bool       { return validHex(id, 32) }
func ValidSourceID(id string) bool { return validHex(id, 64) }
func validHex(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
func ValidTitle(title string) bool {
	return len(title) > 0 && len(title) <= MaxTitleBytes && strings.TrimSpace(title) == title && utf8.ValidString(title) && !strings.ContainsAny(title, "\x00\r\n")
}
func ValidTime(at time.Time) bool { return !at.IsZero() && at.Year() >= 2020 && at.Year() <= 2100 }
func ValidZone(zone string) bool {
	if len(zone) == 0 || len(zone) > 64 || strings.TrimSpace(zone) != zone || strings.ContainsAny(zone, "\x00\r\n\\") || zone == "Local" {
		return false
	}
	_, err := time.LoadLocation(zone)
	return err == nil
}
func (r Reminder) Validate() error {
	if !ValidTime(r.DueAt) || !ValidZone(r.Timezone) || r.Repeat != Once && r.Repeat != Daily && r.Repeat != Weekly {
		return ErrInvalid
	}
	if r.Repeat != Once {
		if r.Hour < 0 || r.Hour > 23 || r.Minute < 0 || r.Minute > 59 || r.Weekday < 0 || r.Weekday > 6 {
			return ErrInvalid
		}
		zone, _ := time.LoadLocation(r.Timezone)
		at := r.DueAt.In(zone)
		if at.Hour() != r.Hour || at.Minute() != r.Minute || at.Second() != 0 || at.Nanosecond() != 0 || r.Repeat == Weekly && at.Weekday() != r.Weekday {
			return ErrInvalid
		}
	}
	return nil
}
func (d Delivery) Validate() error {
	if !d.NextAt.IsZero() && !ValidTime(d.NextAt) {
		return ErrInvalid
	}
	if d.State == "" {
		if !d.At.IsZero() || !d.NextAt.IsZero() || d.Attempted {
			return ErrInvalid
		}
		return nil
	}
	if d.State != NeedsSync && d.State != Scheduling && d.State != Scheduled && d.State != Delivered && d.State != Elapsed && d.State != Blocked && d.State != Failed {
		return ErrInvalid
	}
	if !ValidTime(d.At) {
		return ErrInvalid
	}
	return nil
}
func (i Item) Validate() error {
	if i.RequestHash != "" && !ValidSourceID(i.RequestHash) {
		return ErrInvalid
	}
	if !ValidID(i.ID) || i.Revision == 0 || i.Revision > 1<<48 || !ValidTitle(i.Title) || i.State != Open && i.State != Completed && i.State != Cancelled || !ValidTime(i.CreatedAt) || !ValidTime(i.UpdatedAt) || i.UpdatedAt.Before(i.CreatedAt) || i.UserAuthored == (i.SourceID != "") || i.SourceID != "" && !ValidSourceID(i.SourceID) || i.Delivery.Validate() != nil {
		return ErrInvalid
	}
	if len(i.SourceIDs) > 0 {
		if len(i.SourceIDs) < 2 || len(i.SourceIDs) > MaxSources || i.SourceID != i.SourceIDs[0] || i.UserAuthored {
			return ErrInvalid
		}
		for n, id := range i.SourceIDs {
			if !ValidSourceID(id) || n > 0 && id <= i.SourceIDs[n-1] {
				return ErrInvalid
			}
		}
	}
	if i.Reminder != nil {
		if i.Reminder.Validate() != nil || i.Delivery.State == "" {
			return ErrInvalid
		}
	} else if i.Delivery.State != "" {
		return ErrInvalid
	}
	if len(i.History) > MaxHistory || i.HistoryOmitted > 1<<48 {
		return ErrInvalid
	}
	for n, event := range i.History {
		if event.State != Open && event.State != Completed && event.State != Cancelled || !ValidTime(event.At) || event.At.Before(i.CreatedAt) || event.At.After(i.UpdatedAt) || event.Revision == 0 || event.Revision > i.Revision {
			return ErrInvalid
		}
		if n > 0 && (event.Revision <= i.History[n-1].Revision || event.At.Before(i.History[n-1].At) || event.State == i.History[n-1].State) {
			return ErrInvalid
		}
	}
	if len(i.History) > 0 && i.History[len(i.History)-1].State != i.State {
		return ErrInvalid
	}
	return nil
}
func Clone(i Item) Item {
	i.SourceIDs = append([]string(nil), i.SourceIDs...)
	i.History = append([]Transition(nil), i.History...)
	if i.Reminder != nil {
		reminder := *i.Reminder
		i.Reminder = &reminder
	}
	return i
}

// Sources includes the legacy primary source and all canonical additional
// evidence links. Edits cannot change this provenance.
func (i Item) Sources() []string {
	if len(i.SourceIDs) > 0 {
		return append([]string(nil), i.SourceIDs...)
	}
	if i.SourceID != "" {
		return []string{i.SourceID}
	}
	return nil
}

func (i Item) References(sourceID string) bool {
	if sourceID == "" {
		return false
	}
	if i.SourceID == sourceID {
		return true
	}
	for _, id := range i.SourceIDs {
		if id == sourceID {
			return true
		}
	}
	return false
}
