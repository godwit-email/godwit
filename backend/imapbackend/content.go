package imapbackend

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/godwit-email/godwit/backend"
)

// One round of body-text FETCHes covers at most textBatch messages and asks for
// at most textBatchBytes (or one message's text), which bounds sync memory.
const (
	textBatch      = 50
	textBatchBytes = 32 << 20
)

// maxDepth bounds how deep a structure is converted; mail's own walk stops
// sooner.
const maxDepth = 64

// flagsOf is IMAP's flags written neutrally. \Recent is the session's, not
// the message's. Other flags, \Deleted included, pass as they are spelled.
func flagsOf(fl []imap.Flag) backend.Flags {
	var out backend.Flags
	for _, f := range fl {
		switch f {
		case imap.FlagSeen:
			out.Seen = true
		case imap.FlagFlagged:
			out.Flagged = true
		case imap.FlagAnswered:
			out.Answered = true
		case imap.FlagDraft:
			out.Draft = true
		case "\\Recent":
		default:
			out.Keywords = append(out.Keywords, string(f))
		}
	}
	return out
}

// partOf converts an IMAP body structure; each leaf's Ref is its IMAP part
// path ("1" for a message that is a single part).
func partOf(bs imap.BodyStructure) *backend.Part {
	if bs == nil {
		return nil
	}
	return convert(bs, "", 0)
}

func convert(bs imap.BodyStructure, path string, depth int) *backend.Part {
	switch b := bs.(type) {
	case *imap.BodyStructureMultiPart:
		p := &backend.Part{ContentType: b.MediaType()}
		if depth >= maxDepth {
			return p
		}
		for i, c := range b.Children {
			cp := strconv.Itoa(i + 1)
			if path != "" {
				cp = path + "." + cp
			}
			if child := convert(c, cp, depth+1); child != nil {
				p.Children = append(p.Children, *child)
			}
		}
		return p
	case *imap.BodyStructureSinglePart:
		if path == "" {
			path = "1"
		}
		p := &backend.Part{Ref: backend.PartRef(path), ContentType: b.MediaType(), Params: b.Params, ContentID: b.ID,
			Encoding: strings.ToLower(b.Encoding), Size: int64(b.Size)}
		if d := b.Disposition(); d != nil {
			p.Disposition, p.DispositionParams = strings.ToLower(d.Value), d.Params
		}
		return p
	}
	return nil
}

// sizes maps each leaf of p to its size.
func sizes(p *backend.Part, out map[backend.PartRef]int64) {
	if p == nil {
		return
	}
	if p.Ref != "" {
		out[p.Ref] = p.Size
	}
	for i := range p.Children {
		sizes(&p.Children[i], out)
	}
}

// fillStructures gives each of msgs whose body structure the server sent
// malformed (Gmail, #389; go-imap leaves it nil) one built from the whole
// message, downloaded once, so its text and attachments still import.
func fillStructures(c *imapclient.Client, msgs []*imapclient.FetchMessageBuffer) error {
	whole := &imap.FetchItemBodySection{Peek: true}
	for _, m := range msgs {
		if m.BodyStructure != nil {
			continue
		}
		bufs, err := c.Fetch(imap.UIDSetNum(m.UID), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{whole}}).Collect()
		if err != nil {
			return err
		}
		if len(bufs) == 1 { // none: expunged meanwhile, it imports without parts
			m.BodyStructure = imapserver.ExtractBodyStructure(bytes.NewReader(bufs[0].FindBodySection(whole)))
		}
	}
	return nil
}

// fetched is one message being imported: its buffer, structure and the text
// parts the sync asked for.
type fetched struct {
	m     *imapclient.FetchMessageBuffer
	part  *backend.Part
	texts []backend.PartRef
	cost  int64
}

// partCost is how many bytes fetching a text part of size may return.
func partCost(size, limit int64) int64 {
	if size > 0 && size < limit {
		return size
	}
	return limit
}

// textBatches splits msgs into rounds of at most textBatch messages and
// textBatchBytes of requested text; a message never splits.
func textBatches(msgs []fetched) [][]fetched {
	var out [][]fetched
	var cur []fetched
	var size int64
	for _, m := range msgs {
		if len(cur) > 0 && (len(cur) == textBatch || size+m.cost > textBatchBytes) {
			out, cur, size = append(out, cur), nil, 0
		}
		cur, size = append(cur, m), size+m.cost
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// fetchTexts downloads the first limit bytes of each text part of msgs, keyed
// by UID then part. Messages with the same parts share one UID FETCH.
func fetchTexts(c *imapclient.Client, msgs []fetched, limit int64) (map[imap.UID]map[backend.PartRef][]byte, error) {
	groups := map[string][]imap.UID{}
	layout := map[string][]backend.PartRef{}
	for _, m := range msgs {
		if len(m.texts) == 0 {
			continue
		}
		var key []string
		for _, p := range m.texts {
			key = append(key, string(p))
		}
		k := strings.Join(key, " ")
		groups[k] = append(groups[k], m.m.UID)
		layout[k] = m.texts
	}
	out := map[imap.UID]map[backend.PartRef][]byte{}
	for k, uids := range groups {
		var secs []*imap.FetchItemBodySection
		for _, p := range layout[k] {
			secs = append(secs, &imap.FetchItemBodySection{Part: partPath(p), Peek: true, Partial: &imap.SectionPartial{Size: limit}})
		}
		bufs, err := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true, BodySection: secs}).Collect()
		if err != nil {
			return nil, err
		}
		for _, b := range bufs {
			got := map[backend.PartRef][]byte{}
			for i, s := range secs {
				got[layout[k][i]] = b.FindBodySection(s)
			}
			out[b.UID] = got
		}
	}
	return out, nil
}

func partPath(p backend.PartRef) []int {
	var out []int
	for _, s := range strings.Split(string(p), ".") {
		n, _ := strconv.Atoi(s)
		out = append(out, n)
	}
	return out
}
