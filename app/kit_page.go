package app

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/kit"
)

// kitPage is the dev-only gallery of internal/kit.
type kitPage struct{ g kit.GalleryState }

var _ pageView = (*kitPage)(nil)

func (p *kitPage) Title() (string, string) { return "Kit", "" }
func (p *kitPage) refresh([]events.Event)  {}
func (p *kitPage) Sidebar(*ui.Context)     {}
func (p *kitPage) Content(c *ui.Context)   { kit.Gallery(c, &p.g) }
