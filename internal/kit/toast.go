package kit

import (
	"cmp"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// The kinds of toast, for ui.Toast's Type; "" is a plain one.
const (
	ToastSuccess = "success"
	ToastWarning = "warning"
	ToastError   = "error"
)

// ToastData is ui.Toast's Data for a toast whose action is only valid until
// Deadline (an undo with a durable window): the host hides the action from
// then on, however long the toast itself stays (it stays while hovered).
type ToastData struct{ Deadline time.Time }

// toastPlace is what the page registered this frame to place the toasts by.
type toastPlace struct {
	panel            ui.Element
	bar, field       ui.Rect // in the window, as last laid out
	hasPanel, hasBar bool
	now              time.Time
}

type toastPlaceKey struct{}

func toastPlaceOf(c *ui.Context) *toastPlace {
	return ui.Local(c.Root(), toastPlaceKey{}, func() toastPlace { return toastPlace{} })
}

// ToastAnchor registers panel, the content panel the toasts sit in the
// bottom right corner of; call it every frame, before ToastHost.
func ToastAnchor(c *ui.Context, panel ui.Element) {
	p := toastPlaceOf(c)
	p.panel, p.hasPanel = panel, true
}

// ToastNow registers the time ToastHost judges deadlines by, the app's clock;
// without it, the window's.
func ToastNow(c *ui.Context, now time.Time) { toastPlaceOf(c).now = now }

// ToastBar registers a bar along the panel's foot, and field, its input: the
// toasts rise above bar and line up with field's right edge. Call it every
// frame the bar shows; a frame without it puts them back in the panel's corner.
func ToastBar(c *ui.Context, bar, field ui.Element) { ToastBarRect(c, bar.Bounds(), field.Bounds()) }

// ToastBarRect is ToastBar for a bar drawn by a page in a web view: bar and
// field are where it is in the window.
func ToastBarRect(c *ui.Context, bar, field ui.Rect) {
	p := toastPlaceOf(c)
	p.bar, p.field, p.hasBar = bar, field, true
}

// inset returns how far the toasts' right and bottom edges are from the
// window's, from where the registered elements were the last frame: 16 in
// from the panel's corner (the window's without a panel), or, over a bar, 8
// above its top and level with its input's right edge. measured is false
// until the elements have been laid out once.
func (p toastPlace) inset(c *ui.Context) (right, bottom float32, measured bool) {
	th := c.Theme()
	right, bottom = th.Space(4), th.Space(4)
	win := c.Root().Bounds()
	if win.W == 0 {
		return right, bottom, false
	}
	edgeX, edgeY := win.X+win.W-right, win.Y+win.H-bottom
	measured = true
	if p.hasPanel {
		if b := p.panel.Bounds(); b.W > 0 {
			edgeX, edgeY = b.X+b.W-right, b.Y+b.H-bottom
		} else {
			measured = false
		}
	}
	if p.hasBar {
		if bar, field := p.bar, p.field; bar.W > 0 && field.W > 0 {
			edgeX, edgeY = field.X+field.W, bar.Y-th.Space(2)
		}
		// a field near the window's left (a narrow compose window) keeps the toast inside it
		edgeX = max(edgeX, min(win.X+win.W-th.Space(4), win.X+th.Space(4)+theme.ToastWidth))
	}
	return win.X + win.W - edgeX, win.Y + win.H - edgeY, measured
}

// toastStack is the stack's own state: open is a keyboard expansion, heights
// are the toasts' as the last frame laid them out, by ID.
type toastStack struct {
	open    bool // spread by the keyboard (Enter, or the focus coming in)
	focused bool // the focus was in the stack last frame
	heights map[string]float32
}

// ToastHost draws the window's toasts (c.Toast, c.AddToast) in the bottom
// right corner, where ToastAnchor and ToastBar put it, the newest in front
// and at most theme.ToastMax; call it once in the view, after the page. Only
// the newest is in the layout, so it never moves: the older ones are placed
// against the window beside it, behind it and narrower, showing their edge,
// until the pointer over them or the keyboard focus in them (Enter opens,
// Escape closes) spreads them into a column above it. A toast with a
// ToastData deadline loses its action then. One that goes by itself has no
// close button (Escape closes it); one that stays (Timeout < 0) has, named
// closeLabel (pass the translated "Close"). Its look is always neutral: only
// the leading icon tells a success, warning or error.
func ToastHost(c *ui.Context, closeLabel string) {
	t, th := theme.Of(c), c.Theme()
	place := *toastPlaceOf(c)
	*toastPlaceOf(c) = toastPlace{}
	right, bottom, measured := place.inset(c)
	now := place.now
	if now.IsZero() {
		now = c.Now()
	}
	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		viewport.Padding(0, right, bottom, 0).AlignItems(ui.End).Justify(ui.End)
		if !measured { // placed by where the page was the frame before: wait for it
			viewport.Opacity(0)
			c.Invalidate()
		}
		toasts = toasts[max(0, len(toasts)-theme.ToastMax):]
		if len(toasts) == 0 {
			return
		}
		st := ui.Local(viewport, "stack", func() toastStack { return toastStack{} })
		switch in := viewport.FocusWithin(); {
		case !in:
			st.open, st.focused = false, false
		case !st.focused: // the keyboard came in: spread the stack, as the pointer does
			st.open, st.focused = true, true
		}
		gap := th.Space(2)
		last := toasts[len(toasts)-1]
		prev := st.heights
		heights := map[string]float32{}
		front := prev[last.ID]
		// area covers the stack, gaps between toasts included, to tell the pointer is over it.
		area := ui.Column(c).Absolute().Right(right).Bottom(bottom).Width(theme.ToastWidth).ZIndex(-theme.ToastMax)
		// holder holds the newest toast, in the layout, for the keyboard focus outside the toasts, whose own Escape closes them.
		holder := ui.Column(c)
		if len(toasts) > 1 {
			holder.Focusable()
			switch {
			case st.open && holder.Shortcut(0, ui.KeyEscape):
				st.open = false
			case !st.open && holder.Shortcut(0, ui.KeyEnter):
				st.open = true
			}
		}
		_, _, over := area.PointerPosition()
		expanded := st.open || over || viewport.Hovered()
		extent := front
		if expanded {
			extent = 0
			for _, tst := range toasts {
				extent += cmp.Or(prev[tst.ID], front) + gap
			}
			extent -= gap
		} else {
			extent += gap * float32(len(toasts)-1)
		}
		area.Height(extent)
		build := func(i int, tst ui.Toast) ui.Element {
			toast := ui.ToastBase(c, tst)
			back := len(toasts) - 1 - i // how many toasts are in front of it
			root := toast.Root.Row().AlignItems(ui.Center).Gap(gap).Padding(th.Space(2), th.Space(2), th.Space(2), th.Space(3)).
				TextColor(t.Foreground).FontSize(t.Text.Sm).
				Transition(ui.ElementTransition{Duration: theme.MotionBase, Enter: &ui.Motion{Y: gap}, Exit: &ui.Motion{}})
			toastLook(c, root, t.Radius.Xl)
			root.ZIndex(-back) // the newest paints over the older ones behind it
			heights[tst.ID] = root.LayoutBounds().H
			return root
		}
		for i, tst := range toasts[:len(toasts)-1] {
			back := len(toasts) - 1 - i
			root := build(i, tst)
			if !expanded {
				in := theme.ToastStackInset * float32(back)
				root.Absolute().Right(right + in).Bottom(bottom + gap*float32(back)).Width(theme.ToastWidth - in - in).Height(front)
				continue
			}
			above := float32(0)
			for _, other := range toasts[i+1:] {
				above += cmp.Or(prev[other.ID], front) + gap
			}
			root.Absolute().Right(right).Bottom(bottom + above).Width(theme.ToastWidth)
			root.Children(func() { toastContent(c, tst, closeLabel, now) })
		}
		holder.Children(func() {
			root := build(len(toasts)-1, last).Width(theme.ToastWidth)
			root.Children(func() { toastContent(c, last, closeLabel, now) })
		})
		st.heights = heights
	})
}

// toastContent builds what a toast holds: its icon, its text, its action
// while that is valid, its close button.
func toastContent(c *ui.Context, tst ui.Toast, closeLabel string, now time.Time) {
	t, th := theme.Of(c), c.Theme()
	if icon, color := toastIcon(t, tst.Type); icon != "" {
		ui.Icon(c, icons.Get(icon)).Size(theme.Icon, theme.Icon).TextColor(color).Shrink(0)
	}
	ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(th.Space(0.5)).Children(func() {
		if tst.Description == "" {
			Text(c, tst.Title, t.Text.Sm).MaxLines(2)
			return
		}
		Text(c, tst.Title, t.Text.Sm).FontWeight(500).MaxLines(1)
		Text(c, tst.Description, t.Text.Xs).TextColor(t.Muted).MaxLines(2)
	})
	if tst.Action != "" && toastActionLive(c, tst, now) {
		ui.Box(c).Width(theme.Stroke).Height(th.Space(5)).Background(t.Border).Shrink(0)
		Button(c, tst.Action, ButtonOpts{Variant: Ghost, Size: SizeSm}).Shrink(0).OnClick(func() {
			if tst.OnAction != nil {
				tst.OnAction()
			}
			c.CloseToast(tst.ID)
		})
	}
	if tst.Timeout < 0 {
		IconButton(c, "x", closeLabel, ButtonOpts{Variant: Ghost, Size: SizeSm}).TextColor(t.Muted).Shrink(0).OnClick(func() { c.CloseToast(tst.ID) })
	}
}

// toastActionLive tells whether tst's action is still valid at now, and asks
// for the frame that hides it when it is not yet.
func toastActionLive(c *ui.Context, tst ui.Toast, now time.Time) bool {
	data, _ := tst.Data.(ToastData)
	if data.Deadline.IsZero() {
		return true
	}
	if !now.Before(data.Deadline) {
		return false
	}
	c.After(data.Deadline.Sub(now))
	return true
}

// toastIcon is the leading icon of a toast of typ and its color; a plain
// toast has none.
func toastIcon(t theme.Tokens, typ string) (string, ui.Color) {
	switch typ {
	case ToastSuccess:
		return "circle-check", t.Success
	case ToastWarning:
		return "triangle-alert", t.WarningText
	case ToastError:
		return "circle-x", t.Danger
	}
	return "", t.Foreground
}
