package localapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
)

const (
	rpcProtocolVersion   = 2
	rpcChallengeBytes    = 32
	maxRPCFrameBytes     = 32 << 10
	maxRPCParamsBytes    = 24 << 10
	rpcTimeout           = 3 * time.Second
	RPCSearchMemory      = "search_memory"
	RPCGetToday          = "get_today"
	RPCGetSnapshots      = "get_snapshots"
	RPCGetChronicle      = "get_chronicle"
	RPCListChronicles    = "list_chronicles"
	RPCGetWorkingMemory  = "get_working_memory"
	RPCGetRecentActivity = "get_recent_activity"
	RPCSearchWiki        = "search_wiki"
	RPCGetWikiPage       = "get_wiki_page"
	RPCListTasks         = "list_tasks"
	// parity reads
	RPCReadSession              = "read_session"
	RPCListWiki                 = "list_wiki"
	RPCGetTimeReport            = "get_time_report"
	RPCGetPromises              = "get_promises"
	MaxRPCSearchResponseBytes   = 24 << 10
	MaxRPCSnapshotResponseBytes = 24 << 10
)

// SearchMemoryParams is the bounded request shape for local exact search.
type SearchMemoryParams struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// GetSnapshotsParams reads bounded UTF-8 chunks from source IDs returned by
// search_memory. Offsets are byte offsets and must fall on UTF-8 boundaries.
type GetSnapshotsParams struct {
	SourceIDs         []string       `json:"source_ids"`
	Offsets           map[string]int `json:"offsets,omitempty"`
	MaxBytesPerSource int            `json:"max_bytes_per_source"`
}

// GetChronicleParams reads one source-linked recap. An hourly recap is read by
// its stable HourID. A day or week recap is read with Level ("day" or "week")
// and Period (YYYY-MM-DD or YYYY-Www); HourID is then empty.
type GetChronicleParams struct {
	HourID string `json:"hour_id,omitempty"`
	Level  string `json:"level,omitempty"`
	Period string `json:"period,omitempty"`
}

// ListChroniclesParams reads a bounded, source-linked page of hourly recaps
// from a half-open time range. After is the last hour ID returned previously.
type ListChroniclesParams struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	After string    `json:"after,omitempty"`
	Limit int       `json:"limit"`
}

// GetWorkingMemoryParams reads a small, newest-first set of captures from the
// last Hours of activity. Limit is the maximum number of source excerpts.
type GetWorkingMemoryParams struct {
	Hours int `json:"hours"`
	Limit int `json:"limit"`
}

// GetRecentActivityParams reads a bounded local-calendar activity window.
type GetRecentActivityParams struct {
	DaysBack int `json:"days_back"`
	Limit    int `json:"limit"`
}

// SearchWikiParams searches the source-backed Library directory locally.
type SearchWikiParams struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// GetWikiPageParams reads a bounded page of one Library subject's details.
type GetWikiPageParams struct {
	ID     string `json:"id"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// ListTasksParams reads one bounded page of explicit task state.
type ListTasksParams struct {
	State string `json:"state"`
	Limit int    `json:"limit"`
	After string `json:"after,omitempty"`
}

// ReadSessionParams reads one app's captures inside a window of at most 24
// hours. From and To are Unix seconds (To exclusive). After is the last source
// ID of the previous page; partially read captures continue via get_snapshots.
type ReadSessionParams struct {
	App               string `json:"app"`
	From              int64  `json:"from"`
	To                int64  `json:"to"`
	Limit             int    `json:"limit"`
	After             string `json:"after,omitempty"`
	MaxBytesPerSource int    `json:"max_bytes_per_source,omitempty"`
}

// ListWikiParams lists Library entries, optionally of one kind.
type ListWikiParams struct {
	Type  string `json:"type,omitempty"`
	Limit int    `json:"limit"`
}

// GetTimeReportParams selects a named period or inclusive local dates.
type GetTimeReportParams struct {
	Period string `json:"period,omitempty"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
}

// GetPromisesParams reads the follow-through view over existing records.
type GetPromisesParams struct {
	Query         string `json:"query,omitempty"`
	IncludeClosed bool   `json:"include_closed,omitempty"`
	Limit         int    `json:"limit"`
}

var (
	ErrRPCInvalid      = errors.New("local RPC request invalid")
	ErrRPCUnauthorized = errors.New("local RPC request unauthorized")
	ErrRPCUnavailable  = errors.New("local RPC unavailable")
	ErrRPCOperation    = errors.New("local RPC operation failed")
)

// RPCReadHandler handles one authenticated read-only request. Implementations
// must bound their work and must not log params or returned memory.
type RPCReadHandler func(context.Context, string, json.RawMessage) (json.RawMessage, error)

type rpcBody struct {
	Protocol int             `json:"protocol"`
	ID       string          `json:"id"`
	ClientID string          `json:"client_id"`
	Method   string          `json:"method"`
	Params   json.RawMessage `json:"params"`
}

type rpcProof struct {
	GrantID   string    `json:"grant_id"`
	ClientID  string    `json:"client_id"`
	Scope     Scope     `json:"scope"`
	Timestamp time.Time `json:"timestamp"`
	Challenge []byte    `json:"challenge"`
	Nonce     []byte    `json:"nonce"`
	MAC       []byte    `json:"mac"`
}

type rpcRequest struct {
	rpcBody
	Proof rpcProof `json:"proof"`
}

type rpcResponse struct {
	Protocol int             `json:"protocol"`
	ID       string          `json:"id"`
	Result   json.RawMessage `json:"result,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// RPCClient is a one-request-per-connection client for the bounded local
// protocol. Grant credentials are held only in memory by the caller.
type RPCClient struct {
	mu        sync.RWMutex
	transport LocalTransport
	endpoint  EndpointDescriptor
	grant     IssuedGrant
}

func NewRPCClient(transport LocalTransport, endpoint EndpointDescriptor, grant IssuedGrant) *RPCClient {
	grant.Credential = append([]byte(nil), grant.Credential...)
	grant.Scopes = append([]Scope(nil), grant.Scopes...)
	return &RPCClient{transport: transport, endpoint: endpoint, grant: grant}
}

// Close clears the client-held copy of its bearer credential. Call it after
// the MCP stdio session has stopped processing requests.
func (c *RPCClient) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	clear(c.grant.Credential)
	clear(c.grant.Scopes)
	c.grant = IssuedGrant{}
	c.mu.Unlock()
}

// Call sends one read-only operation and returns one bounded JSON value.
func (c *RPCClient) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if c == nil {
		return nil, ErrRPCInvalid
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.transport == nil || ctx == nil || endpointInvalid(c.endpoint) || !validRPCMethod(method) || !validRPCObject(params) || len(params) > maxRPCParamsBytes || len(c.grant.Credential) != 32 {
		return nil, ErrRPCInvalid
	}
	requestCtx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	id, err := newRPCID()
	if err != nil {
		return nil, ErrRPCUnavailable
	}
	body := rpcBody{Protocol: rpcProtocolVersion, ID: id, ClientID: c.grant.ClientID, Method: method, Params: append(json.RawMessage(nil), params...)}
	bodyBytes, err := json.Marshal(body)
	if err != nil || len(bodyBytes) > maxRPCFrameBytes {
		clear(body.Params)
		return nil, ErrRPCInvalid
	}
	defer clear(bodyBytes)
	defer clear(body.Params)
	conn, err := c.transport.Dial(requestCtx, c.endpoint)
	if err != nil {
		return nil, rpcTransportError(requestCtx, err)
	}
	defer func(close func() error) { _ = close() }(conn.Close)
	challenge, err := conn.Receive(requestCtx)
	if err != nil || len(challenge) != rpcChallengeBytes || allZero(challenge) {
		clear(challenge)
		return nil, rpcTransportError(requestCtx, err)
	}
	defer clear(challenge)
	proof, err := NewProof(c.grant, ScopeMCPBoundedRead, rpcRequestDigest(bodyBytes, challenge), time.Now(), rand.Reader)
	if err != nil {
		return nil, ErrRPCUnauthorized
	}
	defer clear(proof.Nonce)
	frame, err := json.Marshal(rpcRequest{rpcBody: body, Proof: rpcProof{
		GrantID: proof.GrantID, ClientID: proof.ClientID, Scope: proof.Scope,
		Timestamp: proof.Timestamp, Challenge: challenge, Nonce: proof.Nonce, MAC: proof.MAC[:],
	}})
	if err != nil || len(frame) > maxRPCFrameBytes {
		return nil, ErrRPCInvalid
	}
	defer clear(frame)
	if err := conn.Send(requestCtx, frame); err != nil {
		return nil, rpcTransportError(requestCtx, err)
	}
	responseFrame, err := conn.Receive(requestCtx)
	if err != nil || len(responseFrame) > maxRPCFrameBytes {
		clear(responseFrame)
		return nil, rpcTransportError(requestCtx, err)
	}
	defer clear(responseFrame)
	var response rpcResponse
	if decodeRPCJSON(responseFrame, &response) != nil || response.Protocol != rpcProtocolVersion || response.ID != id {
		return nil, ErrRPCInvalid
	}
	switch response.Error {
	case "":
		if !json.Valid(response.Result) || len(response.Result) > maxRPCParamsBytes {
			return nil, ErrRPCInvalid
		}
		return append(json.RawMessage(nil), response.Result...), nil
	case "unauthorized":
		return nil, ErrRPCUnauthorized
	default:
		return nil, ErrRPCOperation
	}
}

// ServeRPC accepts bounded authenticated read-only requests until ctx ends.
// It handles connections serially so a local client cannot create unbounded
// handler goroutines or concurrent store reads.
func ServeRPC(ctx context.Context, transport LocalTransport, endpoint EndpointDescriptor, grants *GrantStore, handler RPCReadHandler) error {
	if ctx == nil || transport == nil || endpointInvalid(endpoint) || grants == nil || handler == nil {
		return ErrRPCInvalid
	}
	listener, err := transport.Listen(ctx, endpoint)
	if err != nil {
		return ErrRPCUnavailable
	}
	defer func(close func() error) { _ = close() }(listener.Close)
	for {
		conn, err := listener.Accept(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return ErrRPCUnavailable
		}
		serveRPCConnection(ctx, conn, grants, handler)
		_ = conn.Close()
	}
}

func serveRPCConnection(ctx context.Context, conn LocalConn, grants *GrantStore, handler RPCReadHandler) {
	requestCtx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	var challenge [rpcChallengeBytes]byte
	if _, err := io.ReadFull(rand.Reader, challenge[:]); err != nil || allZero(challenge[:]) {
		return
	}
	defer clear(challenge[:])
	if err := conn.Send(requestCtx, challenge[:]); err != nil {
		return
	}
	frame, err := conn.Receive(requestCtx)
	if err != nil || len(frame) > maxRPCFrameBytes {
		clear(frame)
		return
	}
	defer clear(frame)
	var request rpcRequest
	if decodeRPCJSON(frame, &request) != nil {
		return
	}
	defer clear(request.Params)
	defer clear(request.Proof.Challenge)
	defer clear(request.Proof.Nonce)
	defer clear(request.Proof.MAC)
	if request.Protocol != rpcProtocolVersion || !validRPCID(request.ID) || !validClientID(request.ClientID) || !validRPCMethod(request.Method) || !validRPCObject(request.Params) || len(request.Params) > maxRPCParamsBytes {
		return
	}
	body := request.rpcBody
	bodyBytes, err := json.Marshal(body)
	if err != nil || len(bodyBytes) > maxRPCFrameBytes {
		return
	}
	defer clear(bodyBytes)
	proof := request.Proof
	if proof.ClientID != request.ClientID || proof.Scope != ScopeMCPBoundedRead || len(proof.Challenge) != len(challenge) || subtle.ConstantTimeCompare(proof.Challenge, challenge[:]) != 1 || len(proof.MAC) != sha256.Size {
		writeRPCError(requestCtx, conn, request.ID, "unauthorized")
		return
	}
	var mac [32]byte
	defer clear(mac[:])
	copy(mac[:], proof.MAC)
	if grants.Authorize(Proof{GrantID: proof.GrantID, ClientID: proof.ClientID, Scope: proof.Scope, Timestamp: proof.Timestamp, Nonce: proof.Nonce, MAC: mac}, rpcRequestDigest(bodyBytes, challenge[:])) != nil {
		writeRPCError(requestCtx, conn, request.ID, "unauthorized")
		return
	}
	params := append(json.RawMessage(nil), request.Params...)
	result, err := handler(requestCtx, request.Method, params)
	clear(params)
	if err != nil || !json.Valid(result) || len(result) > maxRPCParamsBytes {
		writeRPCError(requestCtx, conn, request.ID, "operation_failed")
		return
	}
	response, err := json.Marshal(rpcResponse{Protocol: rpcProtocolVersion, ID: request.ID, Result: result})
	if err != nil || len(response) > maxRPCFrameBytes {
		return
	}
	defer clear(response)
	_ = conn.Send(requestCtx, response)
}

func rpcRequestDigest(body, challenge []byte) [sha256.Size]byte {
	h := sha256.New()
	_, _ = h.Write([]byte("blur-local-rpc-request-v2"))
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(challenge)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(challenge)
	binary.BigEndian.PutUint32(length[:], uint32(len(body)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(body)
	var digest [sha256.Size]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

func writeRPCError(ctx context.Context, conn LocalConn, id, code string) {
	response, err := json.Marshal(rpcResponse{Protocol: rpcProtocolVersion, ID: id, Error: code})
	if err == nil && len(response) <= maxRPCFrameBytes {
		_ = conn.Send(ctx, response)
	}
	clear(response)
}

func decodeRPCJSON(frame []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(frame))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return ErrRPCInvalid
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return ErrRPCInvalid
	}
	return nil
}

func rpcTransportError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ErrCanceled
	}
	if err != nil {
		return ErrRPCUnavailable
	}
	return ErrRPCInvalid
}

func endpointInvalid(endpoint EndpointDescriptor) bool { return endpoint.Validate() != nil }

func validRPCObject(value []byte) bool {
	value = bytes.TrimSpace(value)
	return len(value) > 0 && value[0] == '{' && json.Valid(value)
}

func validRPCMethod(method string) bool {
	return method == RPCSearchMemory || method == RPCGetToday || method == RPCGetSnapshots || method == RPCGetChronicle || method == RPCListChronicles || method == RPCGetWorkingMemory || method == RPCGetRecentActivity || method == RPCSearchWiki || method == RPCGetWikiPage || method == RPCListTasks || method == RPCReadSession || method == RPCListWiki || method == RPCGetTimeReport || method == RPCGetPromises
}

func validRPCID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i := range id {
		c := id[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

func newRPCID() (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return "", err
	}
	const hex = "0123456789abcdef"
	var out [32]byte
	for i, b := range raw {
		out[2*i], out[2*i+1] = hex[b>>4], hex[b&0x0f]
	}
	return string(out[:]), nil
}
