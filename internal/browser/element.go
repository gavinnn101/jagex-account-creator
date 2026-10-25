package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"

	"github.com/mafredri/cdp"
	"github.com/mafredri/cdp/protocol/dom"
	"github.com/mafredri/cdp/protocol/input"
	"github.com/mafredri/cdp/protocol/runtime"
)

// rect is a bounding rectangle from the CDP box model.
type rect struct {
	X      float64
	Y      float64
	Width  float64
	Height float64
}

// EvalJS evaluates the given JavaScript expression and unmarshals the result
// into dest. The expression must produce a JSON-serializable value.
func EvalJS(ctx context.Context, c *cdp.Client, js string, dest any) error {
	return evaluate(ctx, c, runtime.NewEvaluateArgs(js).SetReturnByValue(true), dest)
}

// evalAwaitingJS is EvalJS for an expression whose value is a promise. It awaits the promise.
// The await stays off EvalJS, because an awaiting evaluate on the challenge page delays the Turnstile iframe.
func evalAwaitingJS(ctx context.Context, c *cdp.Client, js string, dest any) error {
	return evaluate(ctx, c, runtime.NewEvaluateArgs(js).SetReturnByValue(true).SetAwaitPromise(true), dest)
}

// evaluate runs evalArgs and unmarshals the result into dest.
func evaluate(ctx context.Context, c *cdp.Client, evalArgs *runtime.EvaluateArgs, dest any) error {
	result, err := c.Runtime.Evaluate(ctx, evalArgs)
	if err != nil {
		return fmt.Errorf("evaluating JS: %w", err)
	}
	if result.ExceptionDetails != nil {
		// Limitation: a synchronous throw reports only "Uncaught". Its message is in Exception.Description.
		return fmt.Errorf("JS exception: %s", result.ExceptionDetails.Text)
	}
	if result.Result.Value == nil {
		return errors.New("JS returned nil")
	}
	if err := json.Unmarshal(result.Result.Value, dest); err != nil {
		return fmt.Errorf("unmarshalling JS result: %w", err)
	}
	return nil
}

// clickAtPoint clicks at (x, y) as a real mouse does: a move, then a press,
// then a release. A press with no move before it is not what a user sends.
func clickAtPoint(ctx context.Context, c *cdp.Client, x, y float64) error {
	// Move mouse to position.
	moveArgs := input.NewDispatchMouseEventArgs("mouseMoved", x, y)
	if err := c.Input.DispatchMouseEvent(ctx, moveArgs); err != nil {
		return fmt.Errorf("mouse move to (%.0f, %.0f): %w", x, y, err)
	}

	// Mouse down.
	downArgs := input.NewDispatchMouseEventArgs("mousePressed", x, y).
		SetButton("left").
		SetClickCount(1)
	if err := c.Input.DispatchMouseEvent(ctx, downArgs); err != nil {
		return fmt.Errorf("mouse down at (%.0f, %.0f): %w", x, y, err)
	}

	// Mouse up.
	upArgs := input.NewDispatchMouseEventArgs("mouseReleased", x, y).
		SetButton("left").
		SetClickCount(1)
	if err := c.Input.DispatchMouseEvent(ctx, upArgs); err != nil {
		return fmt.Errorf("mouse up at (%.0f, %.0f): %w", x, y, err)
	}

	return nil
}

// TurnstileWaitJS is a JavaScript expression that returns true when a Cloudflare
// Turnstile hidden input is present in the DOM. Used with WaitForJS.
const TurnstileWaitJS = `(function() {
	var inputs = document.querySelectorAll('input[type="hidden"]');
	for (var i = 0; i < inputs.length; i++) {
		if (inputs[i].name && inputs[i].name.indexOf('turnstile') !== -1) return true;
	}
	return false;
})();`

// findIframesBySource recursively searches the DOM tree (including shadow
// roots) for iframe elements whose src attribute contains the given substring.
// It returns every match in tree order. A challenge page carries more than one
// Turnstile iframe, so tree order alone does not say which one is clickable.
// Limitation: the walk skips each frame's content document, so it misses an
// iframe inside another frame. A miss fails toward no click.
func findIframesBySource(node dom.Node, srcSubstring string) []dom.NodeID {
	var ids []dom.NodeID
	if node.LocalName == "iframe" {
		for i := 0; i+1 < len(node.Attributes); i += 2 {
			if node.Attributes[i] == "src" && strings.Contains(node.Attributes[i+1], srcSubstring) {
				ids = append(ids, node.NodeID)
				break
			}
		}
	}
	for _, sr := range node.ShadowRoots {
		ids = append(ids, findIframesBySource(sr, srcSubstring)...)
	}
	for _, child := range node.Children {
		ids = append(ids, findIframesBySource(child, srcSubstring)...)
	}
	return ids
}

// isChallengeTitle reports whether the given page title indicates a Cloudflare
// challenge page. siteTitle is the site's own title for its challenge page. An
// empty title is treated as a challenge because the page may still be loading
// the interstitial.
func isChallengeTitle(title, siteTitle string) bool {
	return title == "" || title == "Just a moment..." || title == siteTitle
}

// findCheckboxCoordsOnce does a single attempt to locate the Turnstile
// checkbox coordinates. DOM.GetDocument with pierce=true puts the shadow roots
// in the snapshot. The Turnstile iframe can sit in a shadow root, and
// querySelector cannot enter one. DOM.GetBoxModel then measures each candidate.
// Returns the click point of the first candidate whose box can hold the
// checkbox, or an error naming why each candidate was rejected.
func findCheckboxCoordsOnce(ctx context.Context, c *cdp.Client, logger *slog.Logger) (cx, cy float64, err error) {
	docArgs := dom.NewGetDocumentArgs().SetDepth(-1).SetPierce(true)
	doc, err := c.DOM.GetDocument(ctx, docArgs)
	if err != nil {
		return 0, 0, fmt.Errorf("DOM.getDocument: %w", err)
	}
	nodeIDs := findIframesBySource(doc.Root, "challenges.cloudflare.com")
	if len(nodeIDs) == 0 {
		return 0, 0, errors.New("turnstile iframe not found")
	}

	var rejected []string
	for _, nodeID := range nodeIDs {
		// A candidate that cannot be measured must not hide a usable sibling.
		boxModel, boxErr := c.DOM.GetBoxModel(ctx, dom.NewGetBoxModelArgs().SetNodeID(nodeID))
		if boxErr != nil {
			rejected = append(rejected, fmt.Sprintf("node %d: DOM.getBoxModel: %v", nodeID, boxErr))
			continue
		}
		if len(boxModel.Model.Content) < 8 {
			rejected = append(rejected, fmt.Sprintf("node %d: incomplete box model content quad", nodeID))
			continue
		}

		box := &rect{
			X:      boxModel.Model.Content[0],
			Y:      boxModel.Model.Content[1],
			Width:  boxModel.Model.Content[2] - boxModel.Model.Content[0],
			Height: boxModel.Model.Content[5] - boxModel.Model.Content[1],
		}
		cx, cy = turnstileClickPoint(box)
		if !turnstileBoxFitsCheckbox(box) {
			rejected = append(rejected, fmt.Sprintf("node %d: box %gx%g too small for the checkbox", nodeID, box.Width, box.Height))
			continue
		}
		logger.Debug("turnstile iframe box", "node", nodeID,
			"x", box.X, "y", box.Y, "w", box.Width, "h", box.Height,
			"click_x", cx, "click_y", cy)
		return cx, cy, nil
	}
	return 0, 0, fmt.Errorf("no turnstile iframe fits the checkbox: %d candidates: %s",
		len(nodeIDs), strings.Join(rejected, "; "))
}

// Checkbox geometry inside the Turnstile iframe. The layout is Cloudflare's, so
// a widget redesign fails toward clicks that land beside the checkbox.
const (
	turnstileCheckboxInset = 8  // px from the iframe's left edge
	turnstileCheckboxSize  = 24 // px wide
	turnstileJitterX       = 6  // max px from the checkbox centre
	turnstileJitterY       = 3  // max px from the iframe's vertical centre
)

// turnstileBoxFitsCheckbox reports whether an iframe content box can contain
// the checkbox turnstileClickPoint aims at. The challenge page also serves a
// collapsed Turnstile iframe, and a click on that one lands on bare page.
func turnstileBoxFitsCheckbox(box *rect) bool {
	return box.Width >= turnstileCheckboxInset+turnstileCheckboxSize &&
		box.Height >= turnstileCheckboxSize
}

// turnstileClickPoint computes the viewport coordinates to click for the
// Turnstile checkbox within the given iframe bounds. The checkbox sits near the
// left edge, vertically centered. Jitter keeps the click off a pixel-exact
// centre, which is itself a bot signal, without leaving the checkbox.
func turnstileClickPoint(box *rect) (float64, float64) {
	cx := box.X + turnstileCheckboxInset + turnstileCheckboxSize/2
	cy := box.Y + box.Height/2
	cx += jitter(turnstileJitterX)
	cy += jitter(turnstileJitterY)
	return cx, cy
}

// jitter returns a random offset in [-n, n].
func jitter(n int) float64 {
	return float64(rand.IntN(2*n+1) - n)
}

// clickTurnstile locates the Turnstile checkbox from a fresh DOM snapshot and
// clicks it once. The caller owns the retry loop, so a miss here is not fatal.
func clickTurnstile(ctx context.Context, c *cdp.Client, logger *slog.Logger) error {
	cx, cy, err := findCheckboxCoordsOnce(ctx, c, logger)
	if err != nil {
		return err
	}
	logger.Debug("clicking turnstile checkbox", "x", cx, "y", cy)
	return clickAtPoint(ctx, c, cx, cy)
}

// pageTitle returns document.title, or "" when the read fails. clickIfChallenge
// reads "" as a challenge.
func pageTitle(ctx context.Context, c *cdp.Client) string {
	var title string
	_ = EvalJS(ctx, c, `document.title`, &title)
	return title
}
