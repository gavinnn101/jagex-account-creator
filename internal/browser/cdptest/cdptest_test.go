package cdptest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/mafredri/cdp/protocol/dom"
	"github.com/mafredri/cdp/protocol/input"
	"github.com/mafredri/cdp/protocol/network"
	"github.com/mafredri/cdp/protocol/page"
	"github.com/mafredri/cdp/protocol/runtime"
	"github.com/mafredri/cdp/rpcc"
)

var errFake = errors.New("fake failure")

func TestRuntimeEvaluate(t *testing.T) {
	r := &Runtime{Answer: func(expression string) (*runtime.EvaluateReply, error) {
		if expression == "fail" {
			return nil, errFake
		}
		return Value(`"` + expression + `"`), nil
	}}

	reply, err := r.Evaluate(t.Context(), runtime.NewEvaluateArgs("ok"))
	if err != nil || string(reply.Result.Value) != `"ok"` {
		t.Errorf("Evaluate(ok) = %v, %v, want \"ok\", nil", reply, err)
	}
	if _, err := r.Evaluate(t.Context(), runtime.NewEvaluateArgs("fail")); !errors.Is(err, errFake) {
		t.Errorf("Evaluate(fail) error = %v, want %v", err, errFake)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Evaluate(ctx, runtime.NewEvaluateArgs("late")); !errors.Is(err, context.Canceled) {
		t.Errorf("Evaluate on a done ctx error = %v, want %v", err, context.Canceled)
	}

	got := make([]string, 0, len(r.Calls))
	for _, c := range r.Calls {
		got = append(got, c.Expression)
	}
	if want := []string{"ok", "fail"}; !slices.Equal(got, want) {
		t.Errorf("recorded expressions = %q, want %q", got, want)
	}
}

func TestDOM(t *testing.T) {
	d := &DOM{Boxes: map[dom.NodeID]dom.Quad{7: {1, 2, 3, 4, 5, 6, 7, 8}}}
	d.Document.Root.NodeID = 1

	doc, err := d.GetDocument(t.Context(), dom.NewGetDocumentArgs().SetDepth(-1))
	if err != nil || doc.Root.NodeID != 1 {
		t.Errorf("GetDocument() = %+v, %v, want the root node 1", doc, err)
	}
	d.DocumentErr = errFake
	if _, err := d.GetDocument(t.Context(), dom.NewGetDocumentArgs()); !errors.Is(err, errFake) {
		t.Errorf("GetDocument() error = %v, want %v", err, errFake)
	}
	if len(d.DocumentCalls) != 2 || *d.DocumentCalls[0].Depth != -1 {
		t.Errorf("DocumentCalls = %+v, want two with depth -1 first", d.DocumentCalls)
	}

	box, err := d.GetBoxModel(t.Context(), dom.NewGetBoxModelArgs().SetNodeID(7))
	if err != nil || !slices.Equal(box.Model.Content, d.Boxes[7]) {
		t.Errorf("GetBoxModel(7) = %+v, %v, want the quad of node 7", box, err)
	}
	if _, err := d.GetBoxModel(t.Context(), dom.NewGetBoxModelArgs().SetNodeID(8)); err == nil ||
		err.Error() != "no box model for node 8" {
		t.Errorf("GetBoxModel(8) error = %v, want no box model for node 8", err)
	}
}

func TestInputDispatchMouseEvent(t *testing.T) {
	in := &Input{Errs: map[string]error{"mousePressed": errFake}}

	if err := in.DispatchMouseEvent(t.Context(), input.NewDispatchMouseEventArgs("mouseMoved", 1, 2)); err != nil {
		t.Errorf("mouseMoved error = %v, want nil", err)
	}
	if err := in.DispatchMouseEvent(t.Context(), input.NewDispatchMouseEventArgs("mousePressed", 1, 2)); !errors.Is(err, errFake) {
		t.Errorf("mousePressed error = %v, want %v", err, errFake)
	}
	if len(in.Events) != 2 || in.Events[1].Type != "mousePressed" {
		t.Errorf("Events = %+v, want mouseMoved then mousePressed", in.Events)
	}
}

func TestNetwork(t *testing.T) {
	n := &Network{}

	if err := n.ClearBrowserCookies(t.Context()); err != nil {
		t.Errorf("ClearBrowserCookies() error = %v, want nil", err)
	}
	if err := n.SetBlockedURLs(t.Context(), network.NewSetBlockedURLsArgs([]string{"*.png"})); err != nil {
		t.Errorf("SetBlockedURLs() error = %v, want nil", err)
	}

	n.ClearBrowserCookiesErr, n.SetBlockedURLsErr = errFake, errFake
	if err := n.ClearBrowserCookies(t.Context()); !errors.Is(err, errFake) {
		t.Errorf("ClearBrowserCookies() error = %v, want %v", err, errFake)
	}
	if err := n.SetBlockedURLs(t.Context(), network.NewSetBlockedURLsArgs([]string{"*.css"})); !errors.Is(err, errFake) {
		t.Errorf("SetBlockedURLs() error = %v, want %v", err, errFake)
	}

	want := []string{"ClearBrowserCookies", "SetBlockedURLs", "ClearBrowserCookies", "SetBlockedURLs"}
	if !slices.Equal(n.Calls, want) {
		t.Errorf("Calls = %q, want %q", n.Calls, want)
	}
	if want := [][]string{{"*.png"}, {"*.css"}}; !slices.EqualFunc(n.BlockedURLs, want, slices.Equal) {
		t.Errorf("BlockedURLs = %q, want %q", n.BlockedURLs, want)
	}
}

func TestPageListeners(t *testing.T) {
	p := &Page{
		DOMContent: NewStream[page.DOMContentEventFiredReply](),
		Load:       NewStream[page.LoadEventFiredReply](),
	}
	if s, err := p.DOMContentEventFired(t.Context()); err != nil || s != p.DOMContent {
		t.Errorf("DOMContentEventFired() = %v, %v, want DOMContent", s, err)
	}
	if s, err := p.LoadEventFired(t.Context()); err != nil || s != p.Load {
		t.Errorf("LoadEventFired() = %v, %v, want Load", s, err)
	}

	p.DOMContentErr, p.LoadErr = errFake, errFake
	if _, err := p.DOMContentEventFired(t.Context()); !errors.Is(err, errFake) {
		t.Errorf("DOMContentEventFired() error = %v, want %v", err, errFake)
	}
	if _, err := p.LoadEventFired(t.Context()); !errors.Is(err, errFake) {
		t.Errorf("LoadEventFired() error = %v, want %v", err, errFake)
	}
}

func TestPageCommands(t *testing.T) {
	errText := "net::ERR_FAILED"
	p := &Page{Navigation: page.NavigateReply{ErrorText: &errText}, Screenshot: []byte("png")}

	nav, err := p.Navigate(t.Context(), page.NewNavigateArgs("https://a.example/"))
	if err != nil || nav.ErrorText != &errText {
		t.Errorf("Navigate() = %+v, %v, want Navigation", nav, err)
	}
	shot, err := p.CaptureScreenshot(t.Context(), page.NewCaptureScreenshotArgs())
	if err != nil || string(shot.Data) != "png" {
		t.Errorf("CaptureScreenshot() = %+v, %v, want the Screenshot data", shot, err)
	}

	p.NavigateErr, p.ScreenshotErr = errFake, errFake
	if _, err := p.Navigate(t.Context(), page.NewNavigateArgs("https://b.example/")); !errors.Is(err, errFake) {
		t.Errorf("Navigate() error = %v, want %v", err, errFake)
	}
	if _, err := p.CaptureScreenshot(t.Context(), page.NewCaptureScreenshotArgs()); !errors.Is(err, errFake) {
		t.Errorf("CaptureScreenshot() error = %v, want %v", err, errFake)
	}
	if want := []string{"https://a.example/", "https://b.example/"}; !slices.Equal(p.URLs, want) {
		t.Errorf("URLs = %q, want %q", p.URLs, want)
	}
}

func TestStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first, second := &page.LoadEventFiredReply{Timestamp: 1}, &page.LoadEventFiredReply{Timestamp: 2}
		s := NewStream(first, second)

		for _, want := range []*page.LoadEventFiredReply{first, second} {
			if got, err := s.Recv(); got != want || err != nil {
				t.Errorf("Recv() = %v, %v, want %v, nil", got, err, want)
			}
		}

		recvErr := make(chan error, 1)
		go func() {
			_, err := s.Recv()
			recvErr <- err
		}()
		synctest.Wait()
		select {
		case err := <-recvErr:
			t.Fatalf("Recv() returned %v before Close, want it to wait", err)
		default:
		}

		if err := s.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
		if err := s.Close(); err != nil {
			t.Errorf("second Close() error = %v", err)
		}
		if err := <-recvErr; !errors.Is(err, rpcc.ErrStreamClosing) {
			t.Errorf("Recv() after Close error = %v, want %v", err, rpcc.ErrStreamClosing)
		}
	})
}
