package app

import (
	"math"
	"math/rand/v2"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// Trimmed from mygo's examples/confetti (MIT): canvas-confetti's defaults,
// thrown up once, squares and circles only, slower and wider to stay in
// the window from the sheet's top.
const (
	confettiCount = 50
	confettiTicks = 200 // sixtieths of a second
	confettiLife  = confettiTicks * time.Second / 60
)

// burst is confetti in the air: the whole burst falls and fades, and in it
// each piece bursts out, tilted, and spins, every motion a pose.
type burst struct {
	fall     ui.Pose
	ease     ui.Easing // of the pieces' burst
	pieces   []piece
	launched bool // a frame showed it where it starts, so it heads off
}

type piece struct {
	w, h       float32
	color      int // into confettiColors, picked as it builds to follow the appearance
	circle     bool
	to         ui.Pose
	spin       float32 // degrees it turns in its plane
	tilt, turn float32 // how that plane turns from the viewer
}

// confettiColors are the theme's status hues and Primary, in place of
// canvas-confetti's own palette.
func confettiColors(t theme.Tokens) []ui.Color {
	return append([]ui.Color(nil), t.Success, t.Attention, t.Unread, t.Danger, t.Primary)
}

func newBurst() *burst {
	const decay, ticks, speed = 0.9, float64(confettiTicks), 25.0
	b := &burst{
		fall:   ui.Pose{Y: 3 * confettiTicks, Fade: 1},
		ease:   decayEase(decay, ticks),
		pieces: make([]piece, confettiCount),
	}
	reach := (1 - math.Pow(decay, ticks)) / (1 - decay) // what the decaying speed adds up to
	spread := 90 * math.Pi / 180
	for i := range b.pieces {
		dir := -math.Pi/2 + spread/2 - rand.Float64()*spread
		v := speed*0.5 + rand.Float64()*speed
		p := piece{
			color:  rand.IntN(5),
			circle: rand.IntN(2) == 0,
			to:     ui.Pose{X: float32(math.Cos(dir) * v * reach), Y: float32(math.Sin(dir) * v * reach)},
			tilt:   45 + 40*rand.Float32(),
			turn:   360 * rand.Float32(),
		}
		if p.circle {
			p.w, p.h = 8, 8 // a circle looks the same however it spins
		} else {
			p.w, p.h = 10, 10*(0.5+0.4*rand.Float32())
			turns := (0.6 + 0.8*rand.Float64()) * confettiLife.Seconds()
			if rand.IntN(2) == 0 {
				turns = -turns
			}
			p.spin = float32(turns * 360)
		}
		b.pieces[i] = p
	}
	return b
}

// decayEase is how far a burst has gone, as a fraction of how far it goes,
// t of the way through ticks ticks of its speed keeping decay of itself.
func decayEase(decay, ticks float64) ui.Easing {
	k := math.Log(decay) * ticks
	total := 1 - math.Exp(k)
	return func(t float32) float32 { return float32((1 - math.Exp(k*float64(t))) / total) }
}

// build builds the burst from (x, y) in window DIPs, over everything and
// letting the pointer through. A pose starts at the first one it gets, so
// it shows where it starts for a frame, then heads off.
func (b *burst) build(c *ui.Context, x, y float32) {
	t := theme.Of(c)
	colors := confettiColors(t)
	group := ui.Box(c.Key("confetti")).Absolute().Left(x).Top(y).Size(theme.Hairline, theme.Hairline).PassThrough() // sized to tell it was laid out
	if !b.launched && group.Bounds().W > 0 {
		b.launched = true
	}
	if !b.launched {
		c.AnimationFrame()
	}
	linear := ui.Animation{Duration: confettiLife, Ease: ui.Linear}
	var fall ui.Pose
	if b.launched {
		fall = b.fall
	}
	group.AnimatePose(fall, linear).Children(func() {
		for i := range b.pieces {
			p := &b.pieces[i]
			var to, spin ui.Pose
			if b.launched {
				to, spin = p.to, ui.Pose{Rotate: p.spin}
			}
			shape := func(c *ui.Context) ui.Element {
				e := ui.Box(c).Background(colors[p.color])
				if p.circle {
					e.Radius(t.Radius.Full)
				}
				return e
			}
			var e ui.Element
			if p.spin == 0 {
				e = shape(c.Key(i))
			} else {
				e = ui.Box(c.Key(i))
			}
			// Spun in a plane tilted away, a piece tumbles.
			e.Absolute().Left(-p.w/2).Top(-p.h/2).Size(p.w, p.h).PassThrough().
				Transform(ui.RotationX(p.tilt).Then(ui.Rotation(p.turn))).
				AnimatePose(to, ui.Animation{Duration: confettiLife, Ease: b.ease})
			if p.spin != 0 {
				e.Children(func() { shape(c).Size(p.w, p.h).PassThrough().AnimatePose(spin, linear) })
			}
		}
	})
}
