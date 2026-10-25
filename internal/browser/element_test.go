package browser

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mafredri/cdp"
	"github.com/mafredri/cdp/protocol/dom"
	"github.com/mafredri/cdp/protocol/input"
	"github.com/mafredri/cdp/protocol/runtime"

	"jagex_account_creator/internal/browser/cdptest"
	"jagex_account_creator/internal/logtest"
)

var errFake = errors.New("fake failure")

// staticRuntime returns a fake Runtime that answers every expression with reply and err.
func staticRuntime(reply *runtime.EvaluateReply, err error) *cdptest.Runtime {
	return &cdptest.Runtime{Answer: func(string) (*runtime.EvaluateReply, error) { return reply, err }}
}

// turnstileIframe returns a Turnstile iframe node.
func turnstileIframe(id dom.NodeID) dom.Node {
	return dom.Node{NodeID: id, LocalName: "iframe", Attributes: []string{"src", "https://challenges.cloudflare.com/t"}}
}

// boxQuad returns the content quad of a w by h box at (x, y).
func boxQuad(x, y, w, h float64) dom.Quad {
	return dom.Quad{x, y, x + w, y, x + w, y + h, x, y + h}
}

// The box of the one Turnstile iframe in clickableDOM, at the live widget size.
const clickX, clickY, clickW, clickH = 800.0, 500.0, 300.0, 65.0

// clickableDOM returns a fake DOM whose document holds one Turnstile iframe that fits the checkbox.
func clickableDOM() *cdptest.DOM {
	d := &cdptest.DOM{Boxes: map[dom.NodeID]dom.Quad{5: boxQuad(clickX, clickY, clickW, clickH)}}
	d.Document.Root = dom.Node{NodeID: 1, LocalName: "html", Children: []dom.Node{turnstileIframe(5)}}
	return d
}

// assertClickPoint checks that (x, y) is a jittered click on the checkbox of the clickableDOM box.
func assertClickPoint(t *testing.T, x, y float64) {
	t.Helper()
	centreX := clickX + turnstileCheckboxInset + turnstileCheckboxSize/2
	centreY := clickY + clickH/2
	if x < centreX-turnstileJitterX || x > centreX+turnstileJitterX || y < centreY-turnstileJitterY || y > centreY+turnstileJitterY {
		t.Errorf("click at (%g, %g), want within (%d, %d) of (%g, %g)", x, y, turnstileJitterX, turnstileJitterY, centreX, centreY)
	}
}

// errString returns the text of err, or "" for nil.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// assertNodeIDs compares a findIframesBySource result against the expected
// NodeIDs in tree order. No want values means no match.
func assertNodeIDs(t *testing.T, got []dom.NodeID, want ...dom.NodeID) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("got NodeIDs %v, want %v", got, want)
	}
}

func TestFindIframesBySource_DirectChild(t *testing.T) {
	root := dom.Node{
		NodeID:    1,
		LocalName: "html",
		Children: []dom.Node{
			{
				NodeID:     2,
				LocalName:  "iframe",
				Attributes: []string{"src", "https://challenges.cloudflare.com/turnstile/v0"},
			},
		},
	}

	assertNodeIDs(t, findIframesBySource(root, "challenges.cloudflare.com"), 2)
}

func TestFindIframesBySource_DeeplyNested(t *testing.T) {
	root := dom.Node{
		NodeID:    1,
		LocalName: "html",
		Children: []dom.Node{
			{
				NodeID:    2,
				LocalName: "body",
				Children: []dom.Node{
					{
						NodeID:    3,
						LocalName: "div",
						Children: []dom.Node{
							{
								NodeID:     4,
								LocalName:  "iframe",
								Attributes: []string{"class", "cf-widget", "src", "https://challenges.cloudflare.com/abc"},
							},
						},
					},
				},
			},
		},
	}

	assertNodeIDs(t, findIframesBySource(root, "challenges.cloudflare.com"), 4)
}

func TestFindIframesBySource_InShadowRoot(t *testing.T) {
	root := dom.Node{
		NodeID:    1,
		LocalName: "div",
		ShadowRoots: []dom.Node{
			{
				NodeID:    2,
				LocalName: "#shadow-root",
				Children: []dom.Node{
					{
						NodeID:     3,
						LocalName:  "iframe",
						Attributes: []string{"src", "https://challenges.cloudflare.com/t/v0"},
					},
				},
			},
		},
	}

	assertNodeIDs(t, findIframesBySource(root, "challenges.cloudflare.com"), 3)
}

func TestFindIframesBySource_NestedShadowRoots(t *testing.T) {
	root := dom.Node{
		NodeID:    1,
		LocalName: "body",
		Children: []dom.Node{
			{
				NodeID:    2,
				LocalName: "div",
				ShadowRoots: []dom.Node{
					{
						NodeID:    3,
						LocalName: "#shadow-root",
						Children: []dom.Node{
							{
								NodeID:    4,
								LocalName: "div",
								ShadowRoots: []dom.Node{
									{
										NodeID:    5,
										LocalName: "#shadow-root",
										Children: []dom.Node{
											{
												NodeID:     6,
												LocalName:  "iframe",
												Attributes: []string{"src", "https://challenges.cloudflare.com/deep"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	assertNodeIDs(t, findIframesBySource(root, "challenges.cloudflare.com"), 6)
}

func TestFindIframesBySource_NotFound(t *testing.T) {
	root := dom.Node{
		NodeID:    1,
		LocalName: "html",
		Children: []dom.Node{
			{
				NodeID:     2,
				LocalName:  "iframe",
				Attributes: []string{"src", "https://youtube.com/embed/xyz"},
			},
			{
				NodeID:    3,
				LocalName: "div",
			},
		},
	}

	assertNodeIDs(t, findIframesBySource(root, "challenges.cloudflare.com"))
}

func TestFindIframesBySource_NoIframes(t *testing.T) {
	root := dom.Node{
		NodeID:    1,
		LocalName: "html",
		Children: []dom.Node{
			{NodeID: 2, LocalName: "head"},
			{NodeID: 3, LocalName: "body", Children: []dom.Node{
				{NodeID: 4, LocalName: "div"},
				{NodeID: 5, LocalName: "p"},
			}},
		},
	}

	assertNodeIDs(t, findIframesBySource(root, "challenges.cloudflare.com"))
}

func TestFindIframesBySource_EmptyTree(t *testing.T) {
	root := dom.Node{NodeID: 1, LocalName: "html"}

	assertNodeIDs(t, findIframesBySource(root, "challenges.cloudflare.com"))
}

func TestFindIframesBySource_IframeNoSrc(t *testing.T) {
	root := dom.Node{
		NodeID:    1,
		LocalName: "html",
		Children: []dom.Node{
			{
				NodeID:     2,
				LocalName:  "iframe",
				Attributes: []string{"class", "widget", "id", "my-frame"},
			},
		},
	}

	assertNodeIDs(t, findIframesBySource(root, "challenges.cloudflare.com"))
}

func TestFindIframesBySource_IframeEmptyAttributes(t *testing.T) {
	root := dom.Node{
		NodeID:    1,
		LocalName: "html",
		Children: []dom.Node{
			{
				NodeID:    2,
				LocalName: "iframe",
				// No attributes at all
			},
		},
	}

	assertNodeIDs(t, findIframesBySource(root, "challenges.cloudflare.com"))
}

func TestFindIframesBySource_MultipleIframes(t *testing.T) {
	// The live challenge page carries two Turnstile iframes; the caller needs
	// both so it can pick by box size rather than by tree order.
	root := dom.Node{
		NodeID:    1,
		LocalName: "html",
		Children: []dom.Node{
			{
				NodeID:     10,
				LocalName:  "iframe",
				Attributes: []string{"src", "https://challenges.cloudflare.com/first"},
			},
			{
				NodeID:    11,
				LocalName: "div",
				Children: []dom.Node{
					{
						NodeID:     12,
						LocalName:  "iframe",
						Attributes: []string{"src", "https://youtube.com/embed/xyz"},
					},
				},
			},
			{
				NodeID:     20,
				LocalName:  "iframe",
				Attributes: []string{"src", "https://challenges.cloudflare.com/second"},
			},
		},
	}

	assertNodeIDs(t, findIframesBySource(root, "challenges.cloudflare.com"), 10, 20)
}

func TestFindIframesBySource_MultipleAcrossShadowRoots(t *testing.T) {
	// Shadow roots are walked before children, so a shadow match precedes a
	// child match of the same node.
	root := dom.Node{
		NodeID:    1,
		LocalName: "div",
		ShadowRoots: []dom.Node{
			{
				NodeID:    2,
				LocalName: "#shadow-root",
				Children: []dom.Node{
					{
						NodeID:     3,
						LocalName:  "iframe",
						Attributes: []string{"src", "https://challenges.cloudflare.com/shadow"},
					},
				},
			},
		},
		Children: []dom.Node{
			{
				NodeID:     4,
				LocalName:  "iframe",
				Attributes: []string{"src", "https://challenges.cloudflare.com/child"},
			},
		},
	}

	assertNodeIDs(t, findIframesBySource(root, "challenges.cloudflare.com"), 3, 4)
}

func TestFindIframesBySource_PartialSrcMatch(t *testing.T) {
	root := dom.Node{
		NodeID:    1,
		LocalName: "html",
		Children: []dom.Node{
			{
				NodeID:     2,
				LocalName:  "iframe",
				Attributes: []string{"src", "https://not-challenges.cloudflare.com.evil.com/attack"},
			},
		},
	}

	// strings.Contains will match — this tests the current behavior.
	assertNodeIDs(t, findIframesBySource(root, "challenges.cloudflare.com"), 2)
}

func TestFindIframesBySource_OddAttributeCount(t *testing.T) {
	// An odd number of attributes shouldn't panic; the loop uses i+1 < len check.
	root := dom.Node{
		NodeID:     1,
		LocalName:  "iframe",
		Attributes: []string{"src", "https://challenges.cloudflare.com/v0", "dangling"},
	}

	assertNodeIDs(t, findIframesBySource(root, "challenges.cloudflare.com"), 1)
}

func TestFindIframesBySource_DanglingSrcAttribute(t *testing.T) {
	// A trailing "src" name has no value, so the loop must not read one.
	root := dom.Node{
		NodeID:     1,
		LocalName:  "iframe",
		Attributes: []string{"id", "x", "src"},
	}

	assertNodeIDs(t, findIframesBySource(root, "challenges.cloudflare.com"))
}

func TestFindIframesBySource_DuplicateSrcAttribute(t *testing.T) {
	// A node is reported once even if the substring matches twice.
	root := dom.Node{
		NodeID:     1,
		LocalName:  "iframe",
		Attributes: []string{"src", "https://challenges.cloudflare.com/a", "src", "https://challenges.cloudflare.com/b"},
	}

	assertNodeIDs(t, findIframesBySource(root, "challenges.cloudflare.com"), 1)
}

func TestFindIframesBySource_NonIframeWithMatchingSrc(t *testing.T) {
	// A <div> with src attribute containing the substring should not match.
	root := dom.Node{
		NodeID:    1,
		LocalName: "html",
		Children: []dom.Node{
			{
				NodeID:     2,
				LocalName:  "div",
				Attributes: []string{"src", "https://challenges.cloudflare.com/fake"},
			},
		},
	}

	assertNodeIDs(t, findIframesBySource(root, "challenges.cloudflare.com"))
}

func TestTurnstileBoxFitsCheckbox(t *testing.T) {
	const minWidth = turnstileCheckboxInset + turnstileCheckboxSize
	const minHeight = turnstileCheckboxSize

	tests := []struct {
		name string
		box  rect
		want bool
	}{
		{"live widget geometry", rect{X: 801, Y: 514, Width: 300, Height: 65}, true},
		{"collapsed iframe", rect{X: 951, Y: 548.5, Width: 0, Height: 0}, false},
		{"exact bound", rect{Width: minWidth, Height: minHeight}, true},
		{"one px under width", rect{Width: minWidth - 1, Height: minHeight}, false},
		{"one px under height", rect{Width: minWidth, Height: minHeight - 1}, false},
		{"wide but flat", rect{Width: 300, Height: 0}, false},
		{"tall but narrow", rect{Width: 0, Height: 65}, false},
		{"negative size", rect{Width: -300, Height: -65}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := turnstileBoxFitsCheckbox(&tt.box); got != tt.want {
				t.Errorf("turnstileBoxFitsCheckbox(%.1fx%.1f) = %v, want %v",
					tt.box.Width, tt.box.Height, got, tt.want)
			}
		})
	}
}

func TestTurnstileClickPoint_InsideCheckbox(t *testing.T) {
	// The checkbox occupies [X+inset, X+inset+size] horizontally and the
	// iframe's vertical centre band. Every click must land strictly inside it.
	tests := []struct {
		name string
		box  rect
	}{
		{"live widget geometry", rect{X: 801, Y: 541, Width: 300, Height: 65}},
		{"zero origin", rect{X: 0, Y: 0, Width: 300, Height: 65}},
		{"scrolled down", rect{X: 50, Y: 100, Width: 300, Height: 70}},
		{"fractional origin", rect{X: 802.5, Y: 515.5, Width: 298, Height: 63}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			minX := tt.box.X + turnstileCheckboxInset
			maxX := minX + turnstileCheckboxSize
			centreY := tt.box.Y + tt.box.Height/2

			for range 500 {
				cx, cy := turnstileClickPoint(&tt.box)
				if cx <= minX || cx >= maxX {
					t.Fatalf("cx = %.1f, want strictly inside (%.1f, %.1f)", cx, minX, maxX)
				}
				if cy < centreY-turnstileJitterY || cy > centreY+turnstileJitterY {
					t.Fatalf("cy = %.1f, want within %d of centre %.1f", cy, turnstileJitterY, centreY)
				}
			}
		})
	}
}

func TestTurnstileClickPoint_Jitters(t *testing.T) {
	// A pixel-exact click every time is itself a bot signal, so both axes must
	// vary across calls.
	box := &rect{X: 801, Y: 541, Width: 300, Height: 65}
	firstX, firstY := turnstileClickPoint(box)
	variedX, variedY := false, false

	for range 500 {
		cx, cy := turnstileClickPoint(box)
		if cx != firstX {
			variedX = true
		}
		if cy != firstY {
			variedY = true
		}
	}
	if !variedX {
		t.Errorf("cx never varied across 500 calls (want jitter)")
	}
	if !variedY {
		t.Errorf("cy never varied across 500 calls (want jitter)")
	}
}

func TestJitter_ReachesBothBounds(t *testing.T) {
	const n = 2
	lowest, highest := 0.0, 0.0
	for range 1000 {
		v := jitter(n)
		lowest, highest = min(lowest, v), max(highest, v)
	}
	if lowest != -n || highest != n {
		t.Errorf("jitter(%d) covered [%g, %g] in 1000 calls, want [%d, %d]", n, lowest, highest, -n, n)
	}
}

func TestIsChallengeTitle(t *testing.T) {
	const site = "Security | Example"
	tests := []struct {
		title     string
		siteTitle string
		want      bool
	}{
		{"", site, true},                      // empty = still loading, assume challenge
		{"Just a moment...", site, true},      // classic CF challenge title
		{site, site, true},                    // the site's own challenge title
		{site, "Security | Other", false},     // another site's challenge title
		{"Manage Your Account", site, false},  // real management page
		{"Account Registration", site, false}, // real registration page
		{"just a moment...", site, false},     // case-sensitive — not a match
		{"Security", site, false},             // partial match — not a challenge
		{"Just a moment... ", site, false},    // trailing space — not exact match
		{site + "!", site, false},             // extra char — not exact match
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q/%q", tt.title, tt.siteTitle), func(t *testing.T) {
			if got := isChallengeTitle(tt.title, tt.siteTitle); got != tt.want {
				t.Errorf("isChallengeTitle(%q, %q) = %v, want %v", tt.title, tt.siteTitle, got, tt.want)
			}
		})
	}
}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name    string
		reply   *runtime.EvaluateReply
		err     error
		wantErr string
	}{
		{"call fails", nil, errFake, "evaluating JS: fake failure"},
		// The shape that Chrome sends for a rejected awaited promise.
		{"exception", &runtime.EvaluateReply{ExceptionDetails: &runtime.ExceptionDetails{
			Text: "Uncaught (in promise) Error: boom",
			Exception: &runtime.RemoteObject{
				Type: "object", Subtype: new("error"), ClassName: new("Error"),
				Description: new("Error: boom\n    at <anonymous>:1:16"),
			},
		}}, nil, "JS exception: Uncaught (in promise) Error: boom"},
		{"no value", &runtime.EvaluateReply{}, nil, "JS returned nil"},
		{"value of the wrong type", cdptest.Value(`"text"`), nil, "unmarshalling JS result: json: cannot unmarshal string into Go value of type int"},
		{"value", cdptest.Value(`42`), nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got int
			err := EvalJS(t.Context(), &cdp.Client{Runtime: staticRuntime(tt.reply, tt.err)}, "x", &got)

			if errString(err) != tt.wantErr {
				t.Fatalf("EvalJS() error = %v, want %q", err, tt.wantErr)
			}
			if err == nil && got != 42 {
				t.Errorf("EvalJS() value = %d, want 42", got)
			}
		})
	}
}

func TestEvalArgs(t *testing.T) {
	tests := []struct {
		name      string
		eval      func(*cdp.Client, *int) error
		wantAwait bool
	}{
		{"EvalJS", func(c *cdp.Client, dest *int) error { return EvalJS(t.Context(), c, "js", dest) }, false},
		{"evalAwaitingJS", func(c *cdp.Client, dest *int) error { return evalAwaitingJS(t.Context(), c, "js", dest) }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := staticRuntime(cdptest.Value(`1`), nil)
			var dest int
			if err := tt.eval(&cdp.Client{Runtime: rt}, &dest); err != nil {
				t.Fatal(err)
			}

			want := runtime.NewEvaluateArgs("js").SetReturnByValue(true)
			if tt.wantAwait {
				want.SetAwaitPromise(true)
			}
			if !reflect.DeepEqual(rt.Calls, []runtime.EvaluateArgs{*want}) {
				t.Errorf("Evaluate calls = %+v, want %+v", rt.Calls, *want)
			}
		})
	}
}

func TestPageTitle(t *testing.T) {
	if got := pageTitle(t.Context(), &cdp.Client{Runtime: staticRuntime(cdptest.Value(`"Account"`), nil)}); got != "Account" {
		t.Errorf("pageTitle() = %q, want Account", got)
	}
	if got := pageTitle(t.Context(), &cdp.Client{Runtime: staticRuntime(nil, errFake)}); got != "" {
		t.Errorf("pageTitle() on an error = %q, want empty", got)
	}
}

func TestFindCheckboxCoordsOnce(t *testing.T) {
	tests := []struct {
		name    string
		root    dom.Node
		docErr  error
		boxes   map[dom.NodeID]dom.Quad
		wantErr string
	}{
		{name: "snapshot fails", docErr: errFake, wantErr: "DOM.getDocument: fake failure"},
		{
			name:    "no Turnstile iframe",
			root:    dom.Node{NodeID: 1, LocalName: "html", Children: []dom.Node{{NodeID: 2, LocalName: "iframe"}}},
			wantErr: "turnstile iframe not found",
		},
		{
			name: "every candidate rejected",
			root: dom.Node{NodeID: 1, LocalName: "html", Children: []dom.Node{turnstileIframe(2), turnstileIframe(3), turnstileIframe(4)}},
			boxes: map[dom.NodeID]dom.Quad{
				3: boxQuad(100, 50, 300, 65)[:7],
				4: boxQuad(100, 50, 20, 10),
			},
			wantErr: "no turnstile iframe fits the checkbox: 3 candidates: " +
				"node 2: DOM.getBoxModel: no box model for node 2; " +
				"node 3: incomplete box model content quad; " +
				"node 4: box 20x10 too small for the checkbox",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &cdptest.DOM{DocumentErr: tt.docErr, Boxes: tt.boxes}
			d.Document.Root = tt.root

			_, _, err := findCheckboxCoordsOnce(t.Context(), &cdp.Client{DOM: d}, logtest.New(&bytes.Buffer{}))

			if errString(err) != tt.wantErr {
				t.Errorf("findCheckboxCoordsOnce() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestFindCheckboxCoordsOnceSkipsAnUnmeasuredSibling(t *testing.T) {
	d := clickableDOM()
	d.Document.Root.Children = slices.Insert(d.Document.Root.Children, 0, turnstileIframe(2))
	var buf bytes.Buffer

	x, y, err := findCheckboxCoordsOnce(t.Context(), &cdp.Client{DOM: d}, logtest.New(&buf))
	if err != nil {
		t.Fatal(err)
	}

	assertClickPoint(t, x, y)
	if want := dom.NewGetDocumentArgs().SetDepth(-1).SetPierce(true); !reflect.DeepEqual(d.DocumentCalls, []dom.GetDocumentArgs{*want}) {
		t.Errorf("GetDocument calls = %+v, want %+v", d.DocumentCalls, *want)
	}
	if want := `level=DEBUG msg="turnstile iframe box" node=5 x=800 y=500 w=300 h=65 click_x=`; !strings.HasPrefix(buf.String(), want) {
		t.Errorf("log = %q, want a line that starts %q", buf.String(), want)
	}
}

// mouseClick returns the three events of a left click at (x, y).
func mouseClick(x, y float64) []input.DispatchMouseEventArgs {
	return []input.DispatchMouseEventArgs{
		*input.NewDispatchMouseEventArgs("mouseMoved", x, y),
		*input.NewDispatchMouseEventArgs("mousePressed", x, y).SetButton("left").SetClickCount(1),
		*input.NewDispatchMouseEventArgs("mouseReleased", x, y).SetButton("left").SetClickCount(1),
	}
}

func TestClickAtPoint(t *testing.T) {
	tests := []struct {
		failOn  string
		wantErr string
	}{
		{"", ""},
		{"mouseMoved", "mouse move to (10, 20): fake failure"},
		{"mousePressed", "mouse down at (10, 20): fake failure"},
		{"mouseReleased", "mouse up at (10, 20): fake failure"},
	}
	for _, tt := range tests {
		t.Run("fail on "+tt.failOn, func(t *testing.T) {
			in := &cdptest.Input{Errs: map[string]error{tt.failOn: errFake}}

			err := clickAtPoint(t.Context(), &cdp.Client{Input: in}, 10, 20)

			if errString(err) != tt.wantErr {
				t.Errorf("clickAtPoint() error = %v, want %q", err, tt.wantErr)
			}
			// No event follows a failed one.
			want := mouseClick(10, 20)
			for i, e := range want {
				if e.Type == tt.failOn {
					want = want[:i+1]
					break
				}
			}
			if !reflect.DeepEqual(in.Events, want) {
				t.Errorf("events = %+v, want %+v", in.Events, want)
			}
		})
	}
}

func TestClickTurnstile(t *testing.T) {
	t.Run("clicks the checkbox", func(t *testing.T) {
		in := &cdptest.Input{}
		var buf bytes.Buffer

		if err := clickTurnstile(t.Context(), &cdp.Client{DOM: clickableDOM(), Input: in}, logtest.New(&buf)); err != nil {
			t.Fatal(err)
		}

		if len(in.Events) != 3 {
			t.Fatalf("events = %+v, want one click", in.Events)
		}
		x, y := in.Events[0].X, in.Events[0].Y
		assertClickPoint(t, x, y)
		if !reflect.DeepEqual(in.Events, mouseClick(x, y)) {
			t.Errorf("events = %+v, want a click at (%g, %g)", in.Events, x, y)
		}
		if want := fmt.Sprintf(`level=DEBUG msg="clicking turnstile checkbox" x=%g y=%g`, x, y); !strings.Contains(buf.String(), want) {
			t.Errorf("log = %q, want %q", buf.String(), want)
		}
	})
	t.Run("no checkbox", func(t *testing.T) {
		in := &cdptest.Input{}
		c := &cdp.Client{DOM: &cdptest.DOM{DocumentErr: errFake}, Input: in}

		err := clickTurnstile(t.Context(), c, logtest.New(&bytes.Buffer{}))

		if !errors.Is(err, errFake) || len(in.Events) != 0 {
			t.Errorf("clickTurnstile() = %v with events %+v, want %v and no event", err, in.Events, errFake)
		}
	})
}
