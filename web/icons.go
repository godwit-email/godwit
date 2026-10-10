package web

import "embed"

// RemixIcons are the Remix Icon SVGs Godwit uses, from the remixicon package
// (pinned at 4.8.0, its last Apache-2.0 release), by Godwit name: the line
// style, and the fill style of those a selected rail button or sidebar row
// shows ("mail-fill"). Each value is the path under remix.
var RemixIcons = map[string]string{
	"archive-fill":            "node_modules/remixicon/icons/Business/archive-fill.svg",
	"archive":                 "node_modules/remixicon/icons/Business/archive-line.svg",
	"arrow-left":              "node_modules/remixicon/icons/Arrows/arrow-left-line.svg",
	"arrow-right":             "node_modules/remixicon/icons/Arrows/arrow-right-line.svg",
	"bold":                    "node_modules/remixicon/icons/Editor/bold.svg",
	"check":                   "node_modules/remixicon/icons/System/check-line.svg",
	"chevron-down":            "node_modules/remixicon/icons/Arrows/arrow-down-s-line.svg",
	"chevron-right":           "node_modules/remixicon/icons/Arrows/arrow-right-s-line.svg",
	"circle-check":            "node_modules/remixicon/icons/System/checkbox-circle-line.svg",
	"circle-x":                "node_modules/remixicon/icons/System/close-circle-line.svg",
	"component-fill":          "node_modules/remixicon/icons/System/apps-2-fill.svg",
	"component":               "node_modules/remixicon/icons/System/apps-2-line.svg",
	"copy":                    "node_modules/remixicon/icons/Document/file-copy-line.svg",
	"cpu-fill":                "node_modules/remixicon/icons/Device/cpu-fill.svg",
	"cpu":                     "node_modules/remixicon/icons/Device/cpu-line.svg",
	"download":                "node_modules/remixicon/icons/System/download-line.svg",
	"ellipsis":                "node_modules/remixicon/icons/System/more-line.svg",
	"external-link":           "node_modules/remixicon/icons/System/external-link-line.svg",
	"file-pen-fill":           "node_modules/remixicon/icons/Document/draft-fill.svg",
	"file-pen":                "node_modules/remixicon/icons/Document/draft-line.svg",
	"folder-fill":             "node_modules/remixicon/icons/Document/folder-open-fill.svg",
	"folder":                  "node_modules/remixicon/icons/Document/folder-3-line.svg",
	"forward":                 "node_modules/remixicon/icons/System/share-forward-line.svg",
	"globe":                   "node_modules/remixicon/icons/Business/global-line.svg",
	"image-off":               "node_modules/remixicon/icons/Media/image-line.svg",
	"inbox-fill":              "node_modules/remixicon/icons/Business/inbox-fill.svg",
	"inbox":                   "node_modules/remixicon/icons/Business/inbox-line.svg",
	"info-fill":               "node_modules/remixicon/icons/System/information-fill.svg",
	"info":                    "node_modules/remixicon/icons/System/information-line.svg",
	"italic":                  "node_modules/remixicon/icons/Editor/italic.svg",
	"languages":               "node_modules/remixicon/icons/Editor/translate-2.svg",
	"link":                    "node_modules/remixicon/icons/Editor/link.svg",
	"list-ordered":            "node_modules/remixicon/icons/Editor/list-ordered.svg",
	"list":                    "node_modules/remixicon/icons/Editor/list-unordered.svg",
	"loader":                  "node_modules/remixicon/icons/System/loader-4-line.svg",
	"mail-fill":               "node_modules/remixicon/icons/Business/mail-fill.svg",
	"mail-open":               "node_modules/remixicon/icons/Business/mail-open-line.svg",
	"mail":                    "node_modules/remixicon/icons/Business/mail-line.svg",
	"outbox-fill":             "node_modules/remixicon/icons/Business/inbox-unarchive-fill.svg",
	"outbox":                  "node_modules/remixicon/icons/Business/inbox-unarchive-line.svg",
	"panel-left":              "node_modules/remixicon/icons/Design/layout-left-line.svg",
	"paperclip":               "node_modules/remixicon/icons/Editor/attachment-2.svg",
	"plus":                    "node_modules/remixicon/icons/System/add-line.svg",
	"puzzle-fill":             "node_modules/remixicon/icons/Development/puzzle-fill.svg",
	"puzzle":                  "node_modules/remixicon/icons/Development/puzzle-line.svg",
	"quote":                   "node_modules/remixicon/icons/Editor/double-quotes-l.svg",
	"refresh-cw":              "node_modules/remixicon/icons/System/refresh-line.svg",
	"reply-all":               "node_modules/remixicon/icons/Business/reply-all-line.svg",
	"reply":                   "node_modules/remixicon/icons/Business/reply-line.svg",
	"search":                  "node_modules/remixicon/icons/System/search-line.svg",
	"send-fill":               "node_modules/remixicon/icons/Business/send-plane-fill.svg",
	"send":                    "node_modules/remixicon/icons/Business/send-plane-line.svg",
	"server":                  "node_modules/remixicon/icons/Device/server-line.svg",
	"settings-fill":           "node_modules/remixicon/icons/System/settings-3-fill.svg",
	"settings":                "node_modules/remixicon/icons/System/settings-3-line.svg",
	"shield-fill":             "node_modules/remixicon/icons/System/shield-fill.svg",
	"shield":                  "node_modules/remixicon/icons/System/shield-line.svg",
	"sliders-horizontal-fill": "node_modules/remixicon/icons/Media/equalizer-3-fill.svg",
	"sliders-horizontal":      "node_modules/remixicon/icons/Media/equalizer-3-line.svg",
	"spam-fill":               "node_modules/remixicon/icons/System/spam-2-fill.svg",
	"spam":                    "node_modules/remixicon/icons/System/spam-2-line.svg",
	"sparkles-fill":           "node_modules/remixicon/icons/Weather/sparkling-fill.svg",
	"sparkles":                "node_modules/remixicon/icons/Weather/sparkling-line.svg",
	"square-pen":              "node_modules/remixicon/icons/Design/edit-box-line.svg",
	"star-fill":               "node_modules/remixicon/icons/System/star-fill.svg",
	"star":                    "node_modules/remixicon/icons/System/star-line.svg",
	"trash-2-fill":            "node_modules/remixicon/icons/System/delete-bin-fill.svg",
	"trash-2":                 "node_modules/remixicon/icons/System/delete-bin-line.svg",
	"triangle-alert":          "node_modules/remixicon/icons/System/alert-line.svg",
	"undo-2":                  "node_modules/remixicon/icons/Arrows/arrow-go-back-line.svg",
	"wand-sparkles":           "node_modules/remixicon/icons/Design/magic-line.svg",
	"x":                       "node_modules/remixicon/icons/System/close-line.svg",
}

// remix holds exactly the files of RemixIcons, and the package licence.
//
//go:embed node_modules/remixicon/License
//go:embed node_modules/remixicon/icons/Arrows/arrow-down-s-line.svg
//go:embed node_modules/remixicon/icons/Arrows/arrow-go-back-line.svg
//go:embed node_modules/remixicon/icons/Arrows/arrow-left-line.svg
//go:embed node_modules/remixicon/icons/Arrows/arrow-right-line.svg
//go:embed node_modules/remixicon/icons/Arrows/arrow-right-s-line.svg
//go:embed node_modules/remixicon/icons/Business/archive-fill.svg
//go:embed node_modules/remixicon/icons/Business/archive-line.svg
//go:embed node_modules/remixicon/icons/Business/global-line.svg
//go:embed node_modules/remixicon/icons/Business/inbox-fill.svg
//go:embed node_modules/remixicon/icons/Business/inbox-line.svg
//go:embed node_modules/remixicon/icons/Business/inbox-unarchive-fill.svg
//go:embed node_modules/remixicon/icons/Business/inbox-unarchive-line.svg
//go:embed node_modules/remixicon/icons/Business/mail-fill.svg
//go:embed node_modules/remixicon/icons/Business/mail-line.svg
//go:embed node_modules/remixicon/icons/Business/mail-open-line.svg
//go:embed node_modules/remixicon/icons/Business/reply-all-line.svg
//go:embed node_modules/remixicon/icons/Business/reply-line.svg
//go:embed node_modules/remixicon/icons/Business/send-plane-fill.svg
//go:embed node_modules/remixicon/icons/Business/send-plane-line.svg
//go:embed node_modules/remixicon/icons/Design/edit-box-line.svg
//go:embed node_modules/remixicon/icons/Design/layout-left-line.svg
//go:embed node_modules/remixicon/icons/Design/magic-line.svg
//go:embed node_modules/remixicon/icons/Development/puzzle-fill.svg
//go:embed node_modules/remixicon/icons/Development/puzzle-line.svg
//go:embed node_modules/remixicon/icons/Device/cpu-fill.svg
//go:embed node_modules/remixicon/icons/Device/cpu-line.svg
//go:embed node_modules/remixicon/icons/Device/server-line.svg
//go:embed node_modules/remixicon/icons/Document/draft-fill.svg
//go:embed node_modules/remixicon/icons/Document/draft-line.svg
//go:embed node_modules/remixicon/icons/Document/file-copy-line.svg
//go:embed node_modules/remixicon/icons/Document/folder-3-line.svg
//go:embed node_modules/remixicon/icons/Document/folder-open-fill.svg
//go:embed node_modules/remixicon/icons/Editor/attachment-2.svg
//go:embed node_modules/remixicon/icons/Editor/bold.svg
//go:embed node_modules/remixicon/icons/Editor/double-quotes-l.svg
//go:embed node_modules/remixicon/icons/Editor/italic.svg
//go:embed node_modules/remixicon/icons/Editor/link.svg
//go:embed node_modules/remixicon/icons/Editor/list-ordered.svg
//go:embed node_modules/remixicon/icons/Editor/list-unordered.svg
//go:embed node_modules/remixicon/icons/Editor/translate-2.svg
//go:embed node_modules/remixicon/icons/Media/equalizer-3-fill.svg
//go:embed node_modules/remixicon/icons/Media/equalizer-3-line.svg
//go:embed node_modules/remixicon/icons/Media/image-line.svg
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
//go:embed node_modules/remixicon/icons/System/information-fill.svg
//go:embed node_modules/remixicon/icons/System/information-line.svg
//go:embed node_modules/remixicon/icons/System/loader-4-line.svg
//go:embed node_modules/remixicon/icons/System/more-line.svg
//go:embed node_modules/remixicon/icons/System/refresh-line.svg
//go:embed node_modules/remixicon/icons/System/search-line.svg
//go:embed node_modules/remixicon/icons/System/settings-3-fill.svg
//go:embed node_modules/remixicon/icons/System/settings-3-line.svg
//go:embed node_modules/remixicon/icons/System/share-forward-line.svg
//go:embed node_modules/remixicon/icons/System/shield-fill.svg
//go:embed node_modules/remixicon/icons/System/shield-line.svg
//go:embed node_modules/remixicon/icons/System/spam-2-fill.svg
//go:embed node_modules/remixicon/icons/System/spam-2-line.svg
//go:embed node_modules/remixicon/icons/System/star-fill.svg
//go:embed node_modules/remixicon/icons/System/star-line.svg
//go:embed node_modules/remixicon/icons/Weather/sparkling-fill.svg
//go:embed node_modules/remixicon/icons/Weather/sparkling-line.svg
var remix embed.FS

// RemixIcon returns the SVG of a Godwit icon name, as "trash-2".
func RemixIcon(name string) ([]byte, bool) {
	p, ok := RemixIcons[name]
	if !ok {
		return nil, false
	}
	b, err := remix.ReadFile(p)
	return b, err == nil
}
