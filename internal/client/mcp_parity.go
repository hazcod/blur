package client

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/hazcod/blur/internal/knowledge"
	"github.com/hazcod/blur/internal/localapi"
)

// Parity reads share one dispatcher so the original HandleMCPRead switch only
// needs a single early branch. They are read-only, bounded, and never log
// params or returned memory.

func isMCPParityMethod(method string) bool {
	switch method {
	case localapi.RPCReadSession, localapi.RPCListWiki, localapi.RPCGetTimeReport, localapi.RPCGetPromises:
		return true
	}
	return false
}

func decodeMCPParityParams(raw []byte, dst any) error {
	if len(raw) == 0 || len(raw) > 2<<10 || !json.Valid(raw) {
		return ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(dst) != nil {
		return ErrInvalidRequest
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return ErrInvalidRequest
	}
	return nil
}

func validMCPParityText(value string, maximum int) bool {
	return len(value) <= maximum && utf8.ValidString(value) && strings.IndexByte(value, 0) < 0
}

func validMCPSourceID(id string) bool {
	if len(id) != 64 || strings.ToLower(id) != id {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func (c *Client) handleMCPParityRead(ctx context.Context, method string, raw json.RawMessage) (json.RawMessage, error) {
	var session mcpSessionParams
	var wiki mcpWikiListParams
	var report mcpTimeReportParams
	var promises mcpPromiseParams
	var err error
	switch method {
	case localapi.RPCReadSession:
		session, err = decodeMCPSessionParams(raw)
	case localapi.RPCListWiki:
		wiki, err = decodeMCPWikiListParams(raw)
	case localapi.RPCGetTimeReport:
		report, err = decodeMCPTimeReportParams(raw)
	case localapi.RPCGetPromises:
		promises, err = decodeMCPPromiseParams(raw)
	default:
		err = ErrInvalidRequest
	}
	if err != nil {
		return nil, err
	}
	epoch, stable := c.BeginMemoryRead()
	if !stable {
		return nil, ErrMemoryChanging
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	operation, done, err := c.beginOperation(ctx, noteOperationTimeout)
	if err != nil {
		if canceled := ctx.Err(); canceled != nil {
			return nil, canceled
		}
		return nil, err
	}
	defer done()
	ctx = operation
	switch method {
	case localapi.RPCReadSession:
		return c.readMCPSession(ctx, epoch, session)
	case localapi.RPCListWiki:
		return c.listMCPWiki(ctx, epoch, wiki)
	case localapi.RPCGetTimeReport:
		return c.readMCPTimeReport(ctx, epoch, report)
	default:
		return c.readMCPPromises(ctx, epoch, promises)
	}
}

// publishMCPParity marshals inside the memory-epoch fence so a response never
// outlives a Delete All or store swap.
func (c *Client) publishMCPParity(ctx context.Context, epoch uint64, response any, maxBytes int) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var encoded []byte
	var marshalErr error
	if !c.PublishMemoryRead(epoch, func() { encoded, marshalErr = json.Marshal(response) }) {
		return nil, ErrMemoryChanging
	}
	if marshalErr != nil || len(encoded) > maxBytes {
		clear(encoded)
		return nil, ErrInvalidRequest
	}
	return encoded, nil
}

func mcpParityFits(response any, maxBytes int) bool {
	encoded, err := json.Marshal(response)
	fits := err == nil && len(encoded) <= maxBytes
	clear(encoded)
	return fits
}

// listLibraryEntities reads the complete Library directory (manual, confirmed
// and inferred entries) in bounded pages. Merged entries are omitted because
// their content lives on the destination entry.
func (c *Client) listLibraryEntities(ctx context.Context) ([]knowledge.Entity, error) {
	if c == nil || c.data == nil || ctx == nil {
		return nil, ErrNotConfigured
	}
	var entities []knowledge.Entity
	seenCursor := map[string]bool{}
	seenEntity := map[string]bool{}
	after := ""
	for range knowledge.MaxEntities/50 + 8 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := c.readKnowledgePageContext(ctx, after)
		if err != nil {
			return nil, err
		}
		if _, err := appendKnowledgePage(knowledge.Page{}, page, ""); err != nil || page.HasMore && (page.Next == after || seenCursor[page.Next]) {
			return nil, errKnowledgePage
		}
		for _, entity := range page.Entities {
			if entity.Validate() != nil || seenEntity[entity.ID] {
				return nil, errKnowledgePage
			}
			seenEntity[entity.ID] = true
			if entity.State == knowledge.Merged {
				continue
			}
			entities = append(entities, entity)
		}
		if !page.HasMore {
			return entities, nil
		}
		seenCursor[page.Next] = true
		after = page.Next
	}
	return nil, errKnowledgePage
}
