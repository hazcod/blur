//go:build darwin

package capture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/ebitengine/purego"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/platform"
	"github.com/hazcod/blur/internal/platform/darwin"
	"sync"
	"time"
)

func supportsDocumentSource(source platform.CaptureSource) bool {
	_, ok := source.(*darwin.Adapter)
	return ok
}
func openPlatformDocument(ctx context.Context, p Pipeline, probe platform.CaptureProbe, app, domain string, exclusions Exclusions, at time.Time, ignored func(string) bool) (job DocumentJob, err error) {
	source, ok := p.Source.(*darwin.Adapter)
	if !ok || source == nil {
		return nil, ErrDocumentUnsupported
	}
	nativeAX.once.Do(loadNativeAXAPI)
	if nativeAX.err != nil {
		return nil, captureError(ReadFailed, NativeReadError(ReadStageSetup, 0))
	}
	var root *nativeAXNode
	var page *browserPageLease
	var cursor *axDocumentCursor
	defer func() {
		if err != nil {
			if cursor != nil {
				cursor.close()
			}
			if page != nil {
				page.release()
			}
			if root != nil {
				root.release()
			}
		}
	}()
	err = source.WithVerifiedTarget(ctx, probe.TargetID, func(check context.Context, pid int32) error {
		if manualAXRequests.admit(pid, time.Now()) {
			requestManualAccessibility(check, nativeAX.api, pid)
		}
		window, err := focusedWindow(check, nativeAX.api, pid)
		if err != nil {
			return contextualReadError(ReadStageWindow, err)
		}
		root = &nativeAXNode{api: nativeAX.api, element: window}
		browser := appidentity.IsBrowserApp(app)
		var content axDocumentNode
		if browser {
			if err := validateBrowserWindow(check, nativeAX.api, window); err != nil {
				return captureError(Excluded, err)
			}
			page, err = findBrowserPage(check, root, p.Limits)
			if err != nil {
				return captureError(BrowserUnavailable, err)
			}
			actual, known, err := page.domain(check)
			if err != nil {
				return captureError(DomainUnknown, err)
			}
			// A discovered live web area is the browser authority. Unknown domains
			// cannot satisfy a domain allow/deny policy or enable page Vision fallback.
			if known {
				domain = actual
			}
			if domain == "" && len(exclusions.Domains)+len(exclusions.IncludedDomains)+len(exclusions.RequiredDomains) > 0 {
				return captureError(DomainUnknown, nil)
			}
			node, ok := page.root.(*nativeAXNode)
			if !ok {
				return captureError(ReadFailed, nil)
			}
			content = node
		}
		if !TargetAllowed(exclusions, app, domain) {
			return captureError(Excluded, nil)
		}
		guard := func(check context.Context) error {
			if check == nil {
				return captureError(ReadFailed, nil)
			}
			if err := check.Err(); err != nil {
				return contextError(err)
			}
			if p.CurrentExclusions != nil {
				current, err := p.CurrentExclusions(check)
				if err != nil || !TargetAllowed(current, app, domain) {
					return captureError(Excluded, err)
				}
				if browser && domain == "" && len(current.Domains)+len(current.IncludedDomains)+len(current.RequiredDomains) > 0 {
					return captureError(DomainUnknown, nil)
				}
			}
			return source.WithVerifiedTarget(check, probe.TargetID, func(inner context.Context, pid int32) error {
				if err := validateFocusedWindow(inner, nativeAX.api, pid, window); err != nil {
					return captureError(Excluded, err)
				}
				if page != nil {
					if err := validateBrowserWindow(inner, nativeAX.api, window); err != nil {
						return captureError(Excluded, err)
					}
					if err := page.validate(inner); err != nil {
						return captureError(Excluded, err)
					}
					actual, known, err := page.domain(inner)
					if err != nil || known && actual != domain || !known && domain != "" {
						return captureError(Excluded, errCaptureDomainChanged)
					}
				}
				return nil
			})
		}
		key := sha256.Sum256([]byte(fmt.Sprintf("blur.document.target.v1\x00%s\x00%d\x00%x", app, pid, documentElementHash(window))))
		builder, err := NewCaptureBuilder(app, domain, hex.EncodeToString(key[:]), at)
		if err != nil {
			return err
		}
		if page == nil {
			content = &nativeAXNode{api: root.api, element: root.api.retain(window)}
		} else {
			node := content.(*nativeAXNode)
			content = &nativeAXNode{api: node.api, element: node.api.retain(node.element)}
		}
		cursor, err = newAXDocumentCursor(content, p.Limits.MaxDepth, guard, ignored)
		if err != nil {
			builder.Abort()
			return err
		}
		roundGuard := func(check context.Context) error {
			if presence, ok := p.Source.(platform.CapturePresence); ok {
				idle, err := presence.UserIdle(check)
				if err != nil {
					return captureError(Unavailable, err)
				}
				if idle {
					return captureError(UserIdle, nil)
				}
			}
			return guard(check)
		}
		job = &axCaptureDocumentJob{cursor: cursor, builder: builder, limits: p.Limits, guard: roundGuard, release: func() {
			if page != nil {
				page.release()
			}
			root.release()
		}}
		return nil
	})
	if err != nil {
		if ce := contextError(err); ce != nil {
			return nil, ce
		}
		if _, ok := err.(*CaptureError); ok {
			return nil, err
		}
		return nil, captureError(ReadFailed, err)
	}
	return job, nil
}

var documentHashAPI struct {
	once sync.Once
	hash func(uintptr) uintptr
}

func documentElementHash(element uintptr) uintptr {
	documentHashAPI.once.Do(func() { purego.RegisterLibFunc(&documentHashAPI.hash, nativeAX.api.core, "CFHash") })
	return documentHashAPI.hash(element)
}
