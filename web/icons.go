package web

import "embed"

// RemixIcons are the Remix Icon SVGs Godwit uses, from the remixicon package
// (pinned at 4.8.0, its last Apache-2.0 release), by Godwit name: the line
// style, and the fill style of those a selected rail button or sidebar row
// shows ("mail-fill"). Each value is the path under remix.
var RemixIcons = map[string]string{
	"archive":                   "node_modules/remixicon/icons/Business/archive-line.svg",
	"archive-fill":              "node_modules/remixicon/icons/Business/archive-fill.svg",
	"arrow-left":                "node_modules/remixicon/icons/Arrows/arrow-left-line.svg",
	"arrow-right":               "node_modules/remixicon/icons/Arrows/arrow-right-line.svg",
	"arrow-up":                  "node_modules/remixicon/icons/Arrows/arrow-up-line.svg",
	"arrow-up-right":            "node_modules/remixicon/icons/Arrows/arrow-right-up-line.svg",
	"bold":                      "node_modules/remixicon/icons/Editor/bold.svg",
	"brain-4":                   "node_modules/remixicon/icons/Health & Medical/brain-4-line.svg",
	"brain-4-fill":              "node_modules/remixicon/icons/Health & Medical/brain-4-fill.svg",
	"check":                     "node_modules/remixicon/icons/System/check-line.svg",
	"chevron-down":              "node_modules/remixicon/icons/Arrows/arrow-down-s-line.svg",
	"chevron-right":             "node_modules/remixicon/icons/Arrows/arrow-right-s-line.svg",
	"circle-check":              "node_modules/remixicon/icons/System/checkbox-circle-line.svg",
	"circle-x":                  "node_modules/remixicon/icons/System/close-circle-line.svg",
	"code":                      "node_modules/remixicon/icons/Development/code-s-slash-line.svg",
	"component":                 "node_modules/remixicon/icons/System/apps-2-line.svg",
	"component-fill":            "node_modules/remixicon/icons/System/apps-2-fill.svg",
	"copy":                      "node_modules/remixicon/icons/Document/file-copy-line.svg",
	"cpu":                       "node_modules/remixicon/icons/Device/cpu-line.svg",
	"cpu-fill":                  "node_modules/remixicon/icons/Device/cpu-fill.svg",
	"download":                  "node_modules/remixicon/icons/System/download-line.svg",
	"ellipsis":                  "node_modules/remixicon/icons/System/more-line.svg",
	"external-link":             "node_modules/remixicon/icons/System/external-link-line.svg",
	"eye":                       "node_modules/remixicon/icons/System/eye-line.svg",
	"eye-off":                   "node_modules/remixicon/icons/System/eye-off-line.svg",
	"file-pen":                  "node_modules/remixicon/icons/Document/draft-line.svg",
	"file-pen-fill":             "node_modules/remixicon/icons/Document/draft-fill.svg",
	"file-text":                 "node_modules/remixicon/icons/Document/file-text-line.svg",
	"folder":                    "node_modules/remixicon/icons/Document/folder-3-line.svg",
	"folder-fill":               "node_modules/remixicon/icons/Document/folder-open-fill.svg",
	"forward":                   "node_modules/remixicon/icons/System/share-forward-line.svg",
	"globe":                     "node_modules/remixicon/icons/Business/global-line.svg",
	"grip":                      "node_modules/remixicon/icons/Editor/draggable.svg",
	"image-off":                 "node_modules/remixicon/icons/Media/image-line.svg",
	"inbox":                     "node_modules/remixicon/icons/Business/inbox-line.svg",
	"inbox-fill":                "node_modules/remixicon/icons/Business/inbox-fill.svg",
	"info":                      "node_modules/remixicon/icons/System/information-line.svg",
	"info-fill":                 "node_modules/remixicon/icons/System/information-fill.svg",
	"italic":                    "node_modules/remixicon/icons/Editor/italic.svg",
	"label-bar-chart-box":       "node_modules/remixicon/icons/Business/bar-chart-box-line.svg",
	"label-bill":                "node_modules/remixicon/icons/Document/bill-line.svg",
	"label-book-2":              "node_modules/remixicon/icons/Document/book-2-line.svg",
	"label-braces":              "node_modules/remixicon/icons/Development/braces-line.svg",
	"label-brain":               "node_modules/remixicon/icons/Health & Medical/brain-line.svg",
	"label-briefcase-4":         "node_modules/remixicon/icons/Business/briefcase-4-line.svg",
	"label-calendar-event":      "node_modules/remixicon/icons/Business/calendar-event-line.svg",
	"label-chat-3":              "node_modules/remixicon/icons/Communication/chat-3-line.svg",
	"label-earth":               "node_modules/remixicon/icons/Map/earth-line.svg",
	"label-first-aid-kit":       "node_modules/remixicon/icons/Health & Medical/first-aid-kit-line.svg",
	"label-flask":               "node_modules/remixicon/icons/Health & Medical/flask-line.svg",
	"label-folder-3":            "node_modules/remixicon/icons/Document/folder-3-line.svg",
	"label-footprint":           "node_modules/remixicon/icons/Map/footprint-line.svg",
	"label-global":              "node_modules/remixicon/icons/Business/global-line.svg",
	"label-graduation-cap":      "node_modules/remixicon/icons/Others/graduation-cap-line.svg",
	"label-hammer":              "node_modules/remixicon/icons/Design/hammer-line.svg",
	"label-heart-3":             "node_modules/remixicon/icons/Health & Medical/heart-3-line.svg",
	"label-home-4":              "node_modules/remixicon/icons/Buildings/home-4-line.svg",
	"label-key-2":               "node_modules/remixicon/icons/Others/key-2-line.svg",
	"label-money-dollar-circle": "node_modules/remixicon/icons/Finance/money-dollar-circle-line.svg",
	"label-movie-2":             "node_modules/remixicon/icons/Media/movie-2-line.svg",
	"label-music-2":             "node_modules/remixicon/icons/Media/music-2-line.svg",
	"label-newspaper":           "node_modules/remixicon/icons/Document/newspaper-line.svg",
	"label-notification-3":      "node_modules/remixicon/icons/Media/notification-3-line.svg",
	"label-palette":             "node_modules/remixicon/icons/Design/palette-line.svg",
	"label-pencil":              "node_modules/remixicon/icons/Design/pencil-line.svg",
	"label-plane":               "node_modules/remixicon/icons/Map/plane-line.svg",
	"label-plant":               "node_modules/remixicon/icons/Others/plant-line.svg",
	"label-quill-pen":           "node_modules/remixicon/icons/Design/quill-pen-line.svg",
	"label-run":                 "node_modules/remixicon/icons/Map/run-line.svg",
	"label-scales-3":            "node_modules/remixicon/icons/Others/scales-3-line.svg",
	"label-seedling":            "node_modules/remixicon/icons/Others/seedling-line.svg",
	"label-shield-keyhole":      "node_modules/remixicon/icons/System/shield-keyhole-line.svg",
	"label-shopping-bag-3":      "node_modules/remixicon/icons/Finance/shopping-bag-3-line.svg",
	"label-stethoscope":         "node_modules/remixicon/icons/Health & Medical/stethoscope-line.svg",
	"label-sticky-note":         "node_modules/remixicon/icons/Document/sticky-note-line.svg",
	"label-team":                "node_modules/remixicon/icons/User & Faces/team-line.svg",
	"label-terminal-box":        "node_modules/remixicon/icons/Development/terminal-box-line.svg",
	"label-tools":               "node_modules/remixicon/icons/Design/tools-line.svg",
	"label-truck":               "node_modules/remixicon/icons/Map/truck-line.svg",
	"languages":                 "node_modules/remixicon/icons/Editor/translate-2.svg",
	"link":                      "node_modules/remixicon/icons/Editor/link.svg",
	"list":                      "node_modules/remixicon/icons/Editor/list-unordered.svg",
	"list-ordered":              "node_modules/remixicon/icons/Editor/list-ordered.svg",
	"loader":                    "node_modules/remixicon/icons/System/loader-4-line.svg",
	"lock":                      "node_modules/remixicon/icons/System/lock-line.svg",
	"mail":                      "node_modules/remixicon/icons/Business/mail-line.svg",
	"mail-fill":                 "node_modules/remixicon/icons/Business/mail-fill.svg",
	"mail-open":                 "node_modules/remixicon/icons/Business/mail-open-line.svg",
	"monitor":                   "node_modules/remixicon/icons/Device/computer-line.svg",
	"outbox":                    "node_modules/remixicon/icons/Business/inbox-unarchive-line.svg",
	"outbox-fill":               "node_modules/remixicon/icons/Business/inbox-unarchive-fill.svg",
	"panel-left":                "node_modules/remixicon/icons/Design/layout-left-line.svg",
	"paperclip":                 "node_modules/remixicon/icons/Editor/attachment-2.svg",
	"plus":                      "node_modules/remixicon/icons/System/add-line.svg",
	"printer":                   "node_modules/remixicon/icons/Business/printer-line.svg",
	"puzzle":                    "node_modules/remixicon/icons/Development/puzzle-line.svg",
	"puzzle-fill":               "node_modules/remixicon/icons/Development/puzzle-fill.svg",
	"quill-pen":                 "node_modules/remixicon/icons/Design/quill-pen-line.svg",
	"quill-pen-fill":            "node_modules/remixicon/icons/Design/quill-pen-fill.svg",
	"quote":                     "node_modules/remixicon/icons/Editor/double-quotes-l.svg",
	"refresh-cw":                "node_modules/remixicon/icons/System/refresh-line.svg",
	"reply":                     "node_modules/remixicon/icons/Business/reply-line.svg",
	"reply-all":                 "node_modules/remixicon/icons/Business/reply-all-line.svg",
	"save":                      "node_modules/remixicon/icons/Device/save-line.svg",
	"search":                    "node_modules/remixicon/icons/System/search-line.svg",
	"send":                      "node_modules/remixicon/icons/Business/send-plane-line.svg",
	"send-fill":                 "node_modules/remixicon/icons/Business/send-plane-fill.svg",
	"settings":                  "node_modules/remixicon/icons/System/settings-3-line.svg",
	"settings-fill":             "node_modules/remixicon/icons/System/settings-3-fill.svg",
	"shield":                    "node_modules/remixicon/icons/System/shield-line.svg",
	"shield-fill":               "node_modules/remixicon/icons/System/shield-fill.svg",
	"sliders-horizontal":        "node_modules/remixicon/icons/Media/equalizer-3-line.svg",
	"sliders-horizontal-fill":   "node_modules/remixicon/icons/Media/equalizer-3-fill.svg",
	"spam":                      "node_modules/remixicon/icons/System/spam-2-line.svg",
	"spam-fill":                 "node_modules/remixicon/icons/System/spam-2-fill.svg",
	"sparkles":                  "node_modules/remixicon/icons/Weather/sparkling-line.svg",
	"sparkles-fill":             "node_modules/remixicon/icons/Weather/sparkling-fill.svg",
	"square-pen":                "node_modules/remixicon/icons/Design/edit-box-line.svg",
	"star":                      "node_modules/remixicon/icons/System/star-line.svg",
	"star-fill":                 "node_modules/remixicon/icons/System/star-fill.svg",
	"tag":                       "node_modules/remixicon/icons/Finance/price-tag-3-line.svg",
	"tag-fill":                  "node_modules/remixicon/icons/Finance/price-tag-3-fill.svg",
	"trash-2":                   "node_modules/remixicon/icons/System/delete-bin-line.svg",
	"trash-2-fill":              "node_modules/remixicon/icons/System/delete-bin-fill.svg",
	"triangle-alert":            "node_modules/remixicon/icons/System/alert-line.svg",
	"undo-2":                    "node_modules/remixicon/icons/Arrows/arrow-go-back-line.svg",
	"user":                      "node_modules/remixicon/icons/User & Faces/user-line.svg",
	"wand-sparkles":             "node_modules/remixicon/icons/Design/magic-line.svg",
	"x":                         "node_modules/remixicon/icons/System/close-line.svg",
}

// BrandIcons are the model providers' marks, and the AI apps' that godwit
// mcp connects to (internal/mcpclients' IDs), from @lobehub/icons-static-svg
// (MIT), by Godwit ID. Each value is the path under remix.
var BrandIcons = map[string]string{
	"chatgpt":     "node_modules/@lobehub/icons-static-svg/icons/openai.svg",
	"grok":        "node_modules/@lobehub/icons-static-svg/icons/grok.svg",
	"claude":      "node_modules/@lobehub/icons-static-svg/icons/claude-color.svg",
	"claude-code": "node_modules/@lobehub/icons-static-svg/icons/claudecode-color.svg",
	"codex":       "node_modules/@lobehub/icons-static-svg/icons/codex-color.svg",
	// The same marks in one color, for an app not connected (tinted gray).
	"claude-mono":      "node_modules/@lobehub/icons-static-svg/icons/claude.svg",
	"claude-code-mono": "node_modules/@lobehub/icons-static-svg/icons/claudecode.svg",
	"codex-mono":       "node_modules/@lobehub/icons-static-svg/icons/codex.svg",
}

// remix holds exactly the files of RemixIcons and BrandIcons, and the packages' licences.
//
//go:embed node_modules/remixicon/License
//go:embed node_modules/@lobehub/icons-static-svg/icons/openai.svg
//go:embed node_modules/@lobehub/icons-static-svg/icons/grok.svg
//go:embed node_modules/@lobehub/icons-static-svg/icons/claude-color.svg
//go:embed node_modules/@lobehub/icons-static-svg/icons/claudecode-color.svg
//go:embed node_modules/@lobehub/icons-static-svg/icons/codex-color.svg
//go:embed node_modules/@lobehub/icons-static-svg/icons/claude.svg
//go:embed node_modules/@lobehub/icons-static-svg/icons/claudecode.svg
//go:embed node_modules/@lobehub/icons-static-svg/icons/codex.svg
//go:embed node_modules/remixicon/icons/Arrows/arrow-down-s-line.svg
//go:embed node_modules/remixicon/icons/Development/code-s-slash-line.svg
//go:embed "node_modules/remixicon/icons/Health & Medical/brain-4-line.svg"
//go:embed "node_modules/remixicon/icons/Health & Medical/brain-4-fill.svg"
//go:embed node_modules/remixicon/icons/Arrows/arrow-go-back-line.svg
//go:embed node_modules/remixicon/icons/Arrows/arrow-left-line.svg
//go:embed node_modules/remixicon/icons/Arrows/arrow-right-line.svg
//go:embed node_modules/remixicon/icons/Arrows/arrow-up-line.svg
//go:embed node_modules/remixicon/icons/Arrows/arrow-right-s-line.svg
//go:embed node_modules/remixicon/icons/Arrows/arrow-right-up-line.svg
//go:embed node_modules/remixicon/icons/Buildings/home-4-line.svg
//go:embed node_modules/remixicon/icons/Business/archive-fill.svg
//go:embed node_modules/remixicon/icons/Business/archive-line.svg
//go:embed node_modules/remixicon/icons/Business/bar-chart-box-line.svg
//go:embed node_modules/remixicon/icons/Business/briefcase-4-line.svg
//go:embed node_modules/remixicon/icons/Business/calendar-event-line.svg
//go:embed node_modules/remixicon/icons/Business/global-line.svg
//go:embed node_modules/remixicon/icons/Business/inbox-fill.svg
//go:embed node_modules/remixicon/icons/Business/inbox-line.svg
//go:embed node_modules/remixicon/icons/Business/inbox-unarchive-fill.svg
//go:embed node_modules/remixicon/icons/Business/inbox-unarchive-line.svg
//go:embed node_modules/remixicon/icons/Business/mail-fill.svg
//go:embed node_modules/remixicon/icons/Business/mail-line.svg
//go:embed node_modules/remixicon/icons/Business/mail-open-line.svg
//go:embed node_modules/remixicon/icons/Business/printer-line.svg
//go:embed node_modules/remixicon/icons/Business/reply-all-line.svg
//go:embed node_modules/remixicon/icons/Business/reply-line.svg
//go:embed node_modules/remixicon/icons/Business/send-plane-fill.svg
//go:embed node_modules/remixicon/icons/Business/send-plane-line.svg
//go:embed node_modules/remixicon/icons/Communication/chat-3-line.svg
//go:embed node_modules/remixicon/icons/Design/edit-box-line.svg
//go:embed node_modules/remixicon/icons/Design/hammer-line.svg
//go:embed node_modules/remixicon/icons/Design/layout-left-line.svg
//go:embed node_modules/remixicon/icons/Design/magic-line.svg
//go:embed node_modules/remixicon/icons/Design/palette-line.svg
//go:embed node_modules/remixicon/icons/Design/pencil-line.svg
//go:embed node_modules/remixicon/icons/Design/quill-pen-fill.svg
//go:embed node_modules/remixicon/icons/Design/quill-pen-line.svg
//go:embed node_modules/remixicon/icons/Design/tools-line.svg
//go:embed node_modules/remixicon/icons/Development/braces-line.svg
//go:embed node_modules/remixicon/icons/Development/puzzle-fill.svg
//go:embed node_modules/remixicon/icons/Development/puzzle-line.svg
//go:embed node_modules/remixicon/icons/Development/terminal-box-line.svg
//go:embed node_modules/remixicon/icons/Device/computer-line.svg
//go:embed node_modules/remixicon/icons/Device/cpu-fill.svg
//go:embed node_modules/remixicon/icons/Device/cpu-line.svg
//go:embed node_modules/remixicon/icons/Device/save-line.svg
//go:embed node_modules/remixicon/icons/Document/bill-line.svg
//go:embed node_modules/remixicon/icons/Document/book-2-line.svg
//go:embed node_modules/remixicon/icons/Document/draft-fill.svg
//go:embed node_modules/remixicon/icons/Document/draft-line.svg
//go:embed node_modules/remixicon/icons/Document/file-copy-line.svg
//go:embed node_modules/remixicon/icons/Document/file-text-line.svg
//go:embed node_modules/remixicon/icons/Document/folder-3-line.svg
//go:embed node_modules/remixicon/icons/Document/folder-open-fill.svg
//go:embed node_modules/remixicon/icons/Document/newspaper-line.svg
//go:embed node_modules/remixicon/icons/Document/sticky-note-line.svg
//go:embed node_modules/remixicon/icons/Editor/attachment-2.svg
//go:embed node_modules/remixicon/icons/Editor/bold.svg
//go:embed node_modules/remixicon/icons/Editor/double-quotes-l.svg
//go:embed node_modules/remixicon/icons/Editor/draggable.svg
//go:embed node_modules/remixicon/icons/Editor/italic.svg
//go:embed node_modules/remixicon/icons/Editor/link.svg
//go:embed node_modules/remixicon/icons/Editor/list-ordered.svg
//go:embed node_modules/remixicon/icons/Editor/list-unordered.svg
//go:embed node_modules/remixicon/icons/Editor/translate-2.svg
//go:embed node_modules/remixicon/icons/Finance/money-dollar-circle-line.svg
//go:embed node_modules/remixicon/icons/Finance/price-tag-3-fill.svg
//go:embed node_modules/remixicon/icons/Finance/price-tag-3-line.svg
//go:embed node_modules/remixicon/icons/Finance/shopping-bag-3-line.svg
//go:embed "node_modules/remixicon/icons/Health & Medical/brain-line.svg"
//go:embed "node_modules/remixicon/icons/Health & Medical/first-aid-kit-line.svg"
//go:embed "node_modules/remixicon/icons/Health & Medical/flask-line.svg"
//go:embed "node_modules/remixicon/icons/Health & Medical/heart-3-line.svg"
//go:embed "node_modules/remixicon/icons/Health & Medical/stethoscope-line.svg"
//go:embed node_modules/remixicon/icons/Map/earth-line.svg
//go:embed node_modules/remixicon/icons/Map/footprint-line.svg
//go:embed node_modules/remixicon/icons/Map/plane-line.svg
//go:embed node_modules/remixicon/icons/Map/run-line.svg
//go:embed node_modules/remixicon/icons/Map/truck-line.svg
//go:embed node_modules/remixicon/icons/Media/equalizer-3-fill.svg
//go:embed node_modules/remixicon/icons/Media/equalizer-3-line.svg
//go:embed node_modules/remixicon/icons/Media/image-line.svg
//go:embed node_modules/remixicon/icons/Media/movie-2-line.svg
//go:embed node_modules/remixicon/icons/Media/music-2-line.svg
//go:embed node_modules/remixicon/icons/Media/notification-3-line.svg
//go:embed node_modules/remixicon/icons/Others/graduation-cap-line.svg
//go:embed node_modules/remixicon/icons/Others/key-2-line.svg
//go:embed node_modules/remixicon/icons/Others/plant-line.svg
//go:embed node_modules/remixicon/icons/Others/scales-3-line.svg
//go:embed node_modules/remixicon/icons/Others/seedling-line.svg
//go:embed node_modules/remixicon/icons/System/add-line.svg
//go:embed node_modules/remixicon/icons/System/alert-line.svg
//go:embed node_modules/remixicon/icons/System/apps-2-fill.svg
//go:embed node_modules/remixicon/icons/System/apps-2-line.svg
//go:embed node_modules/remixicon/icons/System/check-line.svg
//go:embed node_modules/remixicon/icons/System/checkbox-circle-line.svg
//go:embed node_modules/remixicon/icons/System/close-circle-line.svg
//go:embed node_modules/remixicon/icons/System/close-line.svg
//go:embed node_modules/remixicon/icons/System/delete-bin-fill.svg
//go:embed node_modules/remixicon/icons/System/delete-bin-line.svg
//go:embed node_modules/remixicon/icons/System/download-line.svg
//go:embed node_modules/remixicon/icons/System/external-link-line.svg
//go:embed node_modules/remixicon/icons/System/eye-line.svg
//go:embed node_modules/remixicon/icons/System/eye-off-line.svg
//go:embed node_modules/remixicon/icons/System/information-fill.svg
//go:embed node_modules/remixicon/icons/System/information-line.svg
//go:embed node_modules/remixicon/icons/System/loader-4-line.svg
//go:embed node_modules/remixicon/icons/System/lock-line.svg
//go:embed node_modules/remixicon/icons/System/more-line.svg
//go:embed node_modules/remixicon/icons/System/refresh-line.svg
//go:embed node_modules/remixicon/icons/System/search-line.svg
//go:embed node_modules/remixicon/icons/System/settings-3-fill.svg
//go:embed node_modules/remixicon/icons/System/settings-3-line.svg
//go:embed node_modules/remixicon/icons/System/share-forward-line.svg
//go:embed node_modules/remixicon/icons/System/shield-fill.svg
//go:embed node_modules/remixicon/icons/System/shield-keyhole-line.svg
//go:embed node_modules/remixicon/icons/System/shield-line.svg
//go:embed node_modules/remixicon/icons/System/spam-2-fill.svg
//go:embed node_modules/remixicon/icons/System/spam-2-line.svg
//go:embed node_modules/remixicon/icons/System/star-fill.svg
//go:embed node_modules/remixicon/icons/System/star-line.svg
//go:embed "node_modules/remixicon/icons/User & Faces/team-line.svg"
//go:embed "node_modules/remixicon/icons/User & Faces/user-line.svg"
//go:embed node_modules/remixicon/icons/Weather/sparkling-fill.svg
//go:embed node_modules/remixicon/icons/Weather/sparkling-line.svg
var remix embed.FS

// RemixIcon returns the SVG of a Godwit icon name, as "trash-2".
func RemixIcon(name string) ([]byte, bool) {
	return readIcon(RemixIcons, name)
}

// BrandIcon returns the SVG of a model provider's mark, as "chatgpt".
func BrandIcon(id string) ([]byte, bool) {
	return readIcon(BrandIcons, id)
}

func readIcon(m map[string]string, name string) ([]byte, bool) {
	p, ok := m[name]
	if !ok {
		return nil, false
	}
	b, err := remix.ReadFile(p)
	return b, err == nil
}
