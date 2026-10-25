// Package cdptest provides fakes of the cdp domain clients for tests. Each fake
// embeds its cdp interface and implements only the methods that the code under
// test calls. A fake is not safe for concurrent use.
package cdptest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/mafredri/cdp"
	"github.com/mafredri/cdp/protocol/dom"
	"github.com/mafredri/cdp/protocol/input"
	"github.com/mafredri/cdp/protocol/network"
	"github.com/mafredri/cdp/protocol/page"
	"github.com/mafredri/cdp/protocol/runtime"
	"github.com/mafredri/cdp/rpcc"
)

// Runtime is a cdp.Runtime whose Evaluate answers from Answer.
type Runtime struct {
	cdp.Runtime

	// Answer returns the reply to one expression.
	Answer func(expression string) (*runtime.EvaluateReply, error)
	// Calls holds the arguments of each Evaluate call, in call order.
	Calls []runtime.EvaluateArgs
}

// Evaluate records args and returns the answer to its expression. On a done
// ctx it returns the context error, as a real call does.
func (r *Runtime) Evaluate(ctx context.Context, args *runtime.EvaluateArgs) (*runtime.EvaluateReply, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.Calls = append(r.Calls, *args)
	return r.Answer(args.Expression)
}

// Value returns an Evaluate reply whose result value is the JSON text raw.
func Value(raw string) *runtime.EvaluateReply {
	return &runtime.EvaluateReply{Result: runtime.RemoteObject{Value: json.RawMessage(raw)}}
}

// DOM is a cdp.DOM that serves one document snapshot and a box per node.
type DOM struct {
	cdp.DOM

	// Document is the reply of GetDocument when DocumentErr is nil.
	Document dom.GetDocumentReply
	// DocumentErr is the error of GetDocument.
	DocumentErr error
	// DocumentCalls holds the arguments of each GetDocument call.
	DocumentCalls []dom.GetDocumentArgs
	// Boxes holds the content quad of each node. GetBoxModel fails for a node with no entry.
	Boxes map[dom.NodeID]dom.Quad
}

// GetDocument records args and returns Document or DocumentErr.
func (d *DOM) GetDocument(_ context.Context, args *dom.GetDocumentArgs) (*dom.GetDocumentReply, error) {
	d.DocumentCalls = append(d.DocumentCalls, *args)
	if d.DocumentErr != nil {
		return nil, d.DocumentErr
	}
	return &d.Document, nil
}

// GetBoxModel returns the box of the node that args names.
func (d *DOM) GetBoxModel(_ context.Context, args *dom.GetBoxModelArgs) (*dom.GetBoxModelReply, error) {
	quad, ok := d.Boxes[*args.NodeID]
	if !ok {
		return nil, fmt.Errorf("no box model for node %d", *args.NodeID)
	}
	return &dom.GetBoxModelReply{Model: dom.BoxModel{Content: quad}}, nil
}

// Input is a cdp.Input that records mouse events.
type Input struct {
	cdp.Input

	// Errs holds the error of each mouse event type, for example "mousePressed".
	Errs map[string]error
	// Events holds the arguments of each DispatchMouseEvent call, the failed one included.
	Events []input.DispatchMouseEventArgs
}

// DispatchMouseEvent records args and returns the error of its event type.
func (i *Input) DispatchMouseEvent(_ context.Context, args *input.DispatchMouseEventArgs) error {
	i.Events = append(i.Events, *args)
	return i.Errs[args.Type]
}

// Network is a cdp.Network that records the cookie clear and the URL block.
type Network struct {
	cdp.Network

	// ClearBrowserCookiesErr is the error of ClearBrowserCookies.
	ClearBrowserCookiesErr error
	// SetBlockedURLsErr is the error of SetBlockedURLs.
	SetBlockedURLsErr error
	// Calls holds the method name of each call, the failed one included, in call order.
	Calls []string
	// BlockedURLs holds the URLs of each SetBlockedURLs call.
	BlockedURLs [][]string
}

// ClearBrowserCookies records the call and returns ClearBrowserCookiesErr.
func (n *Network) ClearBrowserCookies(context.Context) error {
	n.Calls = append(n.Calls, "ClearBrowserCookies")
	return n.ClearBrowserCookiesErr
}

// SetBlockedURLs records the call and its URLs, and returns SetBlockedURLsErr.
func (n *Network) SetBlockedURLs(_ context.Context, args *network.SetBlockedURLsArgs) error {
	n.Calls = append(n.Calls, "SetBlockedURLs")
	n.BlockedURLs = append(n.BlockedURLs, args.URLs)
	return n.SetBlockedURLsErr
}

// Page is a cdp.Page that serves the navigation, its two load events and a screenshot.
type Page struct {
	cdp.Page

	// DOMContent is the stream of DOMContentEventFired when DOMContentErr is nil.
	DOMContent *Stream[page.DOMContentEventFiredReply]
	// DOMContentErr is the error of DOMContentEventFired.
	DOMContentErr error
	// Load is the stream of LoadEventFired when LoadErr is nil.
	Load *Stream[page.LoadEventFiredReply]
	// LoadErr is the error of LoadEventFired.
	LoadErr error

	// Navigation is the reply of Navigate when NavigateErr is nil.
	Navigation page.NavigateReply
	// NavigateErr is the error of Navigate.
	NavigateErr error
	// URLs holds the URL of each Navigate call.
	URLs []string

	// Screenshot is the image data of CaptureScreenshot when ScreenshotErr is nil.
	Screenshot []byte
	// ScreenshotErr is the error of CaptureScreenshot.
	ScreenshotErr error
}

// DOMContentEventFired returns DOMContent or DOMContentErr.
//
//nolint:ireturn // The fake implements a cdp.Page method, which returns the interface.
func (p *Page) DOMContentEventFired(context.Context) (page.DOMContentEventFiredClient, error) {
	if p.DOMContentErr != nil {
		return nil, p.DOMContentErr
	}
	return p.DOMContent, nil
}

// LoadEventFired returns Load or LoadErr.
//
//nolint:ireturn // The fake implements a cdp.Page method, which returns the interface.
func (p *Page) LoadEventFired(context.Context) (page.LoadEventFiredClient, error) {
	if p.LoadErr != nil {
		return nil, p.LoadErr
	}
	return p.Load, nil
}

// Navigate records the URL and returns Navigation or NavigateErr.
func (p *Page) Navigate(_ context.Context, args *page.NavigateArgs) (*page.NavigateReply, error) {
	p.URLs = append(p.URLs, args.URL)
	if p.NavigateErr != nil {
		return nil, p.NavigateErr
	}
	return &p.Navigation, nil
}

// CaptureScreenshot returns Screenshot or ScreenshotErr.
func (p *Page) CaptureScreenshot(context.Context, *page.CaptureScreenshotArgs) (*page.CaptureScreenshotReply, error) {
	if p.ScreenshotErr != nil {
		return nil, p.ScreenshotErr
	}
	return &page.CaptureScreenshotReply{Data: p.Screenshot}, nil
}

// Stream is an event stream of T. Recv returns the queued events in order, then
// blocks until Close. After Close, it returns rpcc.ErrStreamClosing, as a real
// stream does.
type Stream[T any] struct {
	rpcc.Stream

	events    chan *T
	closed    chan struct{}
	closeOnce sync.Once
}

// NewStream returns a stream that holds events. Create it in the synctest
// bubble of its test, so that a Recv that waits for Close blocks durably.
func NewStream[T any](events ...*T) *Stream[T] {
	s := &Stream[T]{events: make(chan *T, len(events)), closed: make(chan struct{})}
	for _, e := range events {
		s.events <- e
	}
	return s
}

// Recv returns the next queued event. With no event queued, it waits for Close.
func (s *Stream[T]) Recv() (*T, error) {
	select {
	case e := <-s.events:
		return e, nil
	default:
	}
	<-s.closed
	return nil, rpcc.ErrStreamClosing
}

// Close ends a waiting Recv. It is safe to call more than once.
func (s *Stream[T]) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}
