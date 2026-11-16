package client

import (
	"reflect"

	"github.com/hazcod/blur/internal/knowledge"
)

// Index progress is shared with Search indexing. It must not turn a browsed
// directory back into the initial loading screen, or interrupt an inspector.
func (d *desktop) refreshLibrary() {
	if d == nil {
		return
	}
	if d.libraryLoading || d.libraryInspect != nil || d.libraryInspectLoading || d.libraryEditFact != "" {
		d.libraryRefreshPending = true
		return
	}
	d.libraryRefreshPending = false
	if d.libraryBrowser.active {
		d.loadLibraryBrowserPage(d.libraryBrowser.page, true)
		return
	}
	d.loadLibraryAfter("")
}

func sameKnowledgePage(a, b knowledge.Page) bool { return reflect.DeepEqual(a, b) }

// Re-read through the user's existing last visible row, including newly added
// entries before it. Keep its continuation instead of collapsing to page one.
// A fully browsed directory stays fully browsed when new entries are appended.
func refreshedKnowledgePage(client *Client, current knowledge.Page) (knowledge.Page, error) {
	boundary := ""
	if current.HasMore && len(current.Entities) > 0 {
		boundary = current.Entities[len(current.Entities)-1].ID
	}
	var page knowledge.Page
	after := ""
	for {
		next, err := client.Knowledge(after)
		if err != nil {
			return knowledge.Page{}, err
		}
		page, err = appendKnowledgePage(page, next, after)
		if err != nil {
			return knowledge.Page{}, err
		}
		if boundary != "" {
			for i, entity := range page.Entities {
				if entity.ID == boundary {
					page.HasMore = page.HasMore || i+1 < len(page.Entities)
					page.Entities = page.Entities[:i+1]
					page.Next = ""
					if page.HasMore {
						page.Next = boundary
					}
					return page, nil
				}
			}
		}
		if !page.HasMore {
			return page, nil
		}
		after = page.Next
	}
}
