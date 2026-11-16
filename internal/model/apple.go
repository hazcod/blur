package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrAppleConfig        = errors.New("model: invalid Apple model configuration")
	ErrAppleUnavailable   = errors.New("model: Apple system model unavailable")
	ErrAppleBudget        = errors.New("model: Apple context budget exceeded")
	ErrAppleResponse      = errors.New("model: invalid Apple model response")
	ErrAppleRefused       = errors.New("model: Apple system model refused request")
	ErrAppleInvalidOutput = errors.New("model: Apple system model returned invalid JSON")
)

const (
	appleHelperName      = "blur-foundation-models"
	appleMaxWireBytes    = 256 << 10
	appleMaxSchemaBytes  = 16 << 10
	appleTimeout         = 45 * time.Second
	appleWaitDelay       = 250 * time.Millisecond
	appleContextCacheTTL = time.Minute
)

// One inference process across all adapters, with interactive priority over
// annotated optional work. Waiting is cancelable and part of the request
// deadline. Status and tokenizer calls do not infer.
var appleInferenceGate applePriorityGate

type AppleStatus struct {
	Available     bool
	Reason        string
	ContextTokens int
	Identity      string
}

// Apple uses a bundled, signed native helper; prompts never enter argv or files.
// Apple controls system model residency, so CloseIdleConnections is a no-op.
type Apple struct {
	helper               string
	command              func(context.Context, string) *exec.Cmd
	sourceTransformation bool
	contextCache         *appleContextCache
}

type appleContextCache struct {
	mu     sync.Mutex
	time   time.Time
	tokens int
}

type appleRequest struct {
	Version              int             `json:"version"`
	Operation            string          `json:"operation"`
	System               string          `json:"system"`
	User                 string          `json:"user"`
	Format               string          `json:"format"`
	Schema               json.RawMessage `json:"schema,omitempty"`
	MaxOutputTokens      int             `json:"max_output_tokens"`
	SourceTransformation bool            `json:"source_transformation,omitempty"`
}

// NewAppleSourceTransformer uses Apple's supported mode for transforming
// existing source material. It does not change the general answer model.
// Callers must still validate the returned JSON and all source authority.
func NewAppleSourceTransformer() (*Apple, error) {
	a, err := NewApple()
	if err != nil {
		return nil, err
	}
	return a.SourceTransformer(), nil
}

// SourceTransformer preserves the selected helper and command while using
// Apple's policy for transformations of supplied source material. A separate
// immutable copy keeps ordinary Ask generation on its original policy.
func (a *Apple) SourceTransformer() *Apple {
	if a == nil {
		return nil
	}
	copy := *a
	copy.sourceTransformation = true
	return &copy
}

type appleResponse struct {
	Version       int     `json:"version"`
	Available     *bool   `json:"available"`
	Reason        *string `json:"reason"`
	ContextTokens *int    `json:"context_tokens"`
	InputTokens   *int    `json:"input_tokens"`
	Identity      *string `json:"identity"`
	Text          *string `json:"text"`
	Error         *string `json:"error"`
}

// NewApple resolves only relative to this executable. It does not execute the
// helper or query model availability, keeping settings validation immediate.
func NewApple() (*Apple, error) {
	if runtime.GOOS != "darwin" {
		return nil, ErrAppleUnavailable
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, ErrAppleUnavailable
	}
	return newApple(appleHelperPath(executable))
}

func appleHelperPath(executable string) string {
	directory := filepath.Dir(executable)
	if filepath.Base(directory) == "MacOS" && filepath.Base(filepath.Dir(directory)) == "Contents" {
		return filepath.Join(filepath.Dir(directory), "Helpers", appleHelperName)
	}
	return filepath.Join(directory, appleHelperName)
}

func newApple(helper string) (*Apple, error) {
	if err := validateAppleHelper(helper); err != nil {
		return nil, err
	}
	return &Apple{helper: helper, contextCache: &appleContextCache{}}, nil
}

func validateAppleHelper(helper string) error {
	info, err := os.Lstat(helper) // #nosec G703 -- The helper path is derived from this executable, not prompts or remote input.
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 {
		return ErrAppleUnavailable
	}
	return nil
}

func (a *Apple) Status(ctx context.Context) (AppleStatus, error) {
	response, err := a.exchange(ctx, appleRequest{Version: 1, Operation: "status", SourceTransformation: a.transformsSources()})
	if err != nil {
		return AppleStatus{}, err
	}
	status := AppleStatus{Available: *response.Available, Reason: *response.Reason}
	if response.ContextTokens != nil {
		status.ContextTokens = *response.ContextTokens
	}
	if response.Identity != nil {
		status.Identity = *response.Identity
	}
	if !status.Available && (*response.Reason != "" && (response.Error == nil || *response.Error == "" || *response.Error == "unavailable" || *response.Error == "unsupported_os")) {
		return status, nil
	}
	if err := appleResponseError(response); err != nil {
		return status, err
	}
	return status, nil
}

func (a *Apple) Identity(ctx context.Context) (string, error) {
	status, err := a.Status(ctx)
	if err != nil {
		return "", err
	}
	if !status.Available {
		return "", ErrAppleUnavailable
	}
	return status.Identity, nil
}

// CountInputTokens reports the actual plain input count. Format/schema guidance
// belongs to CountRequestTokens; runtime capacity belongs to ContextTokenLimit.
func (a *Apple) CountInputTokens(ctx context.Context, system, user string) (int, error) {
	return a.CountRequestTokens(ctx, Request{System: system, User: user})
}

func (a *Apple) ContextTokenLimit(ctx context.Context) (int, error) {
	if ctx == nil {
		return 0, ErrAppleConfig
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if a == nil {
		return 0, ErrAppleUnavailable
	}
	if a.contextCache != nil {
		cache := a.contextCache
		cache.mu.Lock()
		defer cache.mu.Unlock()
		age := time.Since(cache.time)
		if cache.tokens > 0 && age >= 0 && age < appleContextCacheTTL {
			return cache.tokens, nil
		}
		status, err := a.Status(ctx)
		if err != nil {
			return 0, err
		}
		if !status.Available {
			return 0, ErrAppleUnavailable
		}
		cache.tokens, cache.time = status.ContextTokens, time.Now()
		return status.ContextTokens, nil
	}
	status, err := a.Status(ctx)
	if err != nil {
		return 0, err
	}
	if !status.Available {
		return 0, ErrAppleUnavailable
	}
	return status.ContextTokens, nil
}

// CountRequestTokens uses the same format/schema plan as Generate, without
// inference or an output reserve. The helper count operation always receives 0.
func (a *Apple) CountRequestTokens(ctx context.Context, input Request) (int, error) {
	format, err := appleRequestFormat(input, false)
	if err != nil {
		return 0, err
	}
	response, err := a.exchange(ctx, appleRequest{Version: 1, Operation: "count", System: input.System, User: input.User, Format: format, Schema: input.Schema, SourceTransformation: a.transformsSources()})
	if err != nil {
		return 0, err
	}
	if err := appleResponseError(response); err != nil {
		return 0, err
	}
	return *response.InputTokens, nil
}

// EstimateRequestTokens uses a byte-count upper bound to avoid a separate
// helper process for ordinary short questions. Large inputs fall back to the
// native tokenizer so the service can pack them tightly against context size.
func (a *Apple) EstimateRequestTokens(input Request) (int, bool) {
	format, err := appleRequestFormat(input, false)
	if err != nil {
		return 0, false
	}
	count := len(input.System) + len(input.User)
	if format == "json" {
		count += len("\nReturn only one valid JSON object, without Markdown fences or commentary.")
		if len(input.Schema) > 0 {
			count += len("\nMatch this JSON schema exactly:\n") + len(input.Schema)
		}
	}
	return count, true
}

func (a *Apple) Generate(ctx context.Context, input Request) (Response, error) {
	format, err := appleRequestFormat(input, true)
	if err != nil {
		return Response{}, err
	}
	response, err := a.exchange(ctx, appleRequest{Version: 1, Operation: "generate", System: input.System, User: input.User, Format: format, Schema: input.Schema, MaxOutputTokens: input.MaxOutputTokens, SourceTransformation: a.transformsSources()})
	if err != nil {
		return Response{}, err
	}
	if err := appleResponseError(response); err != nil {
		if errors.Is(err, ErrAppleUnavailable) || errors.Is(err, ErrAppleBudget) {
			a.invalidateContextLimit()
		}
		return Response{}, err
	}
	return Response{Text: *response.Text, InputTokens: *response.InputTokens}, nil
}

func (a *Apple) invalidateContextLimit() {
	if a == nil || a.contextCache == nil {
		return
	}
	a.contextCache.mu.Lock()
	a.contextCache.tokens, a.contextCache.time = 0, time.Time{}
	a.contextCache.mu.Unlock()
}

func appleRequestFormat(input Request, generating bool) (string, error) {
	if !validAppleText(input.System, input.User) || input.MaxOutputTokens < 0 || input.MaxOutputTokens > MaxCompletionTokens || input.KeepAlive < 0 || input.KeepAlive > 10*time.Minute || input.Format != "" && input.Format != "json" || generating && (input.MaxOutputTokens == 0 || input.System == "" || input.User == "") {
		return "", ErrAppleConfig
	}
	format := input.Format
	if len(input.Schema) != 0 {
		if len(input.Schema) > appleMaxSchemaBytes || !validAppleJSONObject(input.Schema) {
			return "", ErrAppleConfig
		}
		format = "json"
	}
	return format, nil
}

func (a *Apple) CloseIdleConnections() {}

func (a *Apple) transformsSources() bool { return a != nil && a.sourceTransformation }

var _ LocalCompletion = (*Apple)(nil)
var _ TokenCapacity = (*Apple)(nil)
var _ RequestTokenCounter = (*Apple)(nil)

func validAppleText(system, user string) bool {
	return len(system)+len(user) <= MaxRequestBytes && utf8.ValidString(system) && utf8.ValidString(user) && !strings.ContainsRune(system, 0) && !strings.ContainsRune(user, 0)
}

func validAppleJSONObject(data []byte) bool {
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return false
	}
	var object map[string]any
	if json.Unmarshal(data, &object) != nil || object == nil {
		return false
	}
	var valid func(any, int) bool
	valid = func(value any, depth int) bool {
		if depth > 64 {
			return false
		}
		switch value := value.(type) {
		case string:
			return !strings.ContainsRune(value, 0)
		case map[string]any:
			for key, child := range value {
				if strings.ContainsRune(key, 0) || !valid(child, depth+1) {
					return false
				}
			}
		case []any:
			for _, child := range value {
				if !valid(child, depth+1) {
					return false
				}
			}
		}
		return true
	}
	return valid(object, 0)
}

func (a *Apple) exchange(ctx context.Context, request appleRequest) (appleResponse, error) {
	if ctx == nil {
		return appleResponse{}, ErrAppleConfig
	}
	if err := ctx.Err(); err != nil {
		return appleResponse{}, err
	}
	if a == nil || a.helper == "" {
		return appleResponse{}, ErrAppleUnavailable
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > appleMaxWireBytes {
		return appleResponse{}, ErrAppleConfig
	}
	defer clear(encoded)
	if err := validateAppleHelper(a.helper); err != nil {
		return appleResponse{}, err
	}
	deadline, cancel := context.WithTimeout(ctx, appleTimeout)
	defer cancel()
	if request.Operation == "generate" {
		run, release, err := appleInferenceGate.acquire(deadline)
		if err != nil {
			if err := ctx.Err(); err != nil {
				return appleResponse{}, err
			}
			return appleResponse{}, err
		}
		deadline = run
		// Defer holds ownership through cmd.Run/Wait and response validation.
		defer release()
	}
	if err := ctx.Err(); err != nil {
		return appleResponse{}, err
	}
	if err := deadline.Err(); err != nil {
		return appleResponse{}, context.Cause(deadline)
	}
	command := a.command
	if command == nil {
		command = func(ctx context.Context, path string) *exec.Cmd {
			// #nosec G702 -- Execute the validated bundled helper directly; model text never enters argv.
			return exec.CommandContext(ctx, path)
		}
	}
	cmd := command(deadline, a.helper)
	if cmd == nil {
		return appleResponse{}, ErrAppleUnavailable
	}
	stdout := &appleBoundedOutput{cancel: cancel}
	cmd.Stdin = bytes.NewReader(encoded)
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
	cmd.WaitDelay = appleWaitDelay
	err = cmd.Run()
	body, overflow := stdout.result()
	defer clear(body)
	if err := ctx.Err(); err != nil {
		return appleResponse{}, err
	}
	if overflow {
		return appleResponse{}, ErrAppleResponse
	}
	if err := deadline.Err(); err != nil {
		return appleResponse{}, context.Cause(deadline)
	}
	if err != nil {
		return appleResponse{}, ErrAppleUnavailable
	}
	return decodeAppleResponse(body, request)
}

type appleBoundedOutput struct {
	mu       sync.Mutex
	data     []byte
	overflow bool
	cancel   context.CancelFunc
}

func (output *appleBoundedOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.overflow || len(output.data)+len(data) > MaxResponseBytes {
		output.overflow = true
		output.cancel()
		return 0, ErrAppleResponse
	}
	output.data = append(output.data, data...)
	return len(data), nil
}

func (output *appleBoundedOutput) result() ([]byte, bool) {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.data, output.overflow
}

func decodeAppleResponse(body []byte, request appleRequest) (appleResponse, error) {
	var response appleResponse
	if len(body) == 0 || !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 || !validAppleEnvelope(body) {
		return response, ErrAppleResponse
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&response) != nil {
		return appleResponse{}, ErrAppleResponse
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return appleResponse{}, ErrAppleResponse
	}
	if response.Version != 1 || response.Available == nil || response.Reason == nil || !validAppleReason(*response.Reason, *response.Available) {
		return appleResponse{}, ErrAppleResponse
	}
	if response.ContextTokens != nil && (*response.ContextTokens <= PromptTemplateReserve || *response.ContextTokens > ContextTokenLimit) || response.InputTokens != nil && (*response.InputTokens < 0 || *response.InputTokens > appleMaxWireBytes) || response.Identity != nil && !validAppleIdentity(*response.Identity) || response.Text != nil && (len(*response.Text) > MaxGeneratedContentBytes || !utf8.ValidString(*response.Text) || strings.ContainsRune(*response.Text, 0)) {
		return appleResponse{}, ErrAppleResponse
	}
	if response.Error != nil {
		switch *response.Error {
		case "", "invalid_request", "unavailable", "context_budget", "refused", "generation_failed", "invalid_json", "unsupported_os":
		default:
			return appleResponse{}, ErrAppleResponse
		}
	}
	if !*response.Available {
		if response.Text != nil || *response.Reason == "" && (response.Error == nil || *response.Error != "invalid_request") {
			return appleResponse{}, ErrAppleResponse
		}
		return response, nil
	}
	if response.ContextTokens == nil || response.Identity == nil {
		return appleResponse{}, ErrAppleResponse
	}
	if response.Error != nil && *response.Error != "" {
		if response.Text != nil {
			return appleResponse{}, ErrAppleResponse
		}
		return response, nil
	}
	switch request.Operation {
	case "status":
		if response.Text != nil || response.InputTokens != nil {
			return appleResponse{}, ErrAppleResponse
		}
	case "count":
		if response.InputTokens == nil || response.Text != nil {
			return appleResponse{}, ErrAppleResponse
		}
	case "generate":
		if response.InputTokens == nil || response.Text == nil || len(*response.Text) == 0 || *response.InputTokens+PromptTemplateReserve+request.MaxOutputTokens > *response.ContextTokens || request.Format == "json" && !validAppleJSONObject([]byte(*response.Text)) {
			return appleResponse{}, ErrAppleResponse
		}
	default:
		return appleResponse{}, ErrAppleResponse
	}
	return response, nil
}

// encoding/json otherwise accepts duplicate keys and treats null pointers like
// omitted fields. Neither occurs in the helper's protocol; reject both.
func validAppleEnvelope(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return false
	}
	seen := make(map[string]bool, 8)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return false
		}
		seen[key] = true
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return false
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return false
	}
	var trailing any
	return decoder.Decode(&trailing) == io.EOF
}

func validAppleIdentity(identity string) bool {
	return len(identity) > 0 && len(identity) <= 256 && utf8.ValidString(identity) && strings.IndexFunc(identity, unicode.IsControl) < 0
}

func validAppleReason(reason string, available bool) bool {
	if available {
		return reason == "available"
	}
	switch reason {
	case "", "device_not_eligible", "apple_intelligence_not_enabled", "model_not_ready", "unavailable", "unsupported_os":
		return true
	default:
		return false
	}
}

func appleResponseError(response appleResponse) error {
	if response.Error != nil {
		switch *response.Error {
		case "invalid_request":
			return ErrAppleConfig
		case "unavailable", "unsupported_os":
			return ErrAppleUnavailable
		case "context_budget":
			return ErrAppleBudget
		case "refused":
			return ErrAppleRefused
		case "generation_failed":
			return ErrAppleResponse
		case "invalid_json":
			return ErrAppleInvalidOutput
		}
	}
	if !*response.Available {
		return ErrAppleUnavailable
	}
	return nil
}
