package uiclient

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/mail"
)

// InlineImages streams cached disk paths from InlineImagesStream and reads each
// inline image file locally without transferring image bytes over the wire.
func (c *Client) InlineImages(ctx context.Context, id int64, each func(cid string, a mail.Attachment)) error {
	return consumeStream(c, ctx, "InlineImagesStream", nil, func(it core.InlineImage) error {
		if each != nil {
			if data, err := os.ReadFile(it.Path); err == nil {
				each(it.CID, mail.Attachment{Data: data, ContentType: it.ContentType})
			}
		}
		return nil
	}, id)
}

// FetchAttachments streams cached disk paths from FetchAttachmentsStream and
// invokes fn with each local file path in order.
func (c *Client) FetchAttachments(ctx context.Context, attachmentIDs []int64, fn func(path string, info core.AttachmentInfo) error) error {
	return consumeStream(c, ctx, "FetchAttachmentsStream", nil, func(a core.FetchedAttachment) error {
		if fn != nil {
			return fn(a.Path, a.Info)
		}
		return nil
	}, attachmentIDs)
}

// InstallPluginFromRegistryWithProgress streams progress updates from
// InstallPluginFromRegistryStream and returns the installed PluginInfo.
func (c *Client) InstallPluginFromRegistryWithProgress(ctx context.Context, name string, progress func(core.PluginInstallProgress)) (core.PluginInfo, error) {
	var installed *core.PluginInfo
	err := consumeStream(c, ctx, "InstallPluginFromRegistryStream", progressOnly[core.PluginInstallEvent](func(ev core.PluginInstallEvent) bool { return ev.Plugin == nil }), func(ev core.PluginInstallEvent) error {
		if ev.Plugin != nil {
			installed = ev.Plugin
		} else if progress != nil {
			progress(ev.Progress)
		}
		return nil
	}, name)
	if err != nil {
		return core.PluginInfo{}, err
	}
	if installed == nil {
		return core.PluginInfo{}, errors.New("uiclient: InstallPluginFromRegistryStream ended without PluginInfo")
	}
	return *installed, nil
}

// PrepareModelWithProgress streams status updates from PrepareModelStream and
// returns the final ModelStatus.
func (c *Client) PrepareModelWithProgress(ctx context.Context, ref string, progress func(core.ModelStatus)) (core.ModelStatus, error) {
	var final core.ModelStatus
	var done bool
	err := consumeStream(c, ctx, "PrepareModelStream", progressOnly[core.PrepareModelEvent](func(ev core.PrepareModelEvent) bool { return !ev.Done }), func(ev core.PrepareModelEvent) error {
		if ev.Done {
			final = ev.Status
			done = true
		} else if progress != nil {
			progress(ev.Status)
		}
		return nil
	}, ref)
	if err != nil {
		return core.ModelStatus{}, err
	}
	if !done {
		return core.ModelStatus{}, errors.New("uiclient: PrepareModelStream ended without final status")
	}
	return final, nil
}

// DownloadRecognitionModel streams byte progress from
// DownloadRecognitionModelStream.
func (c *Client) DownloadRecognitionModel(ctx context.Context, progress func(got, total int64)) error {
	return consumeStream(c, ctx, "DownloadRecognitionModelStream", progressOnly[core.RecognitionModelProgress](func(core.RecognitionModelProgress) bool { return true }), func(p core.RecognitionModelProgress) error {
		if progress != nil {
			progress(p.Got, p.Total)
		}
		return nil
	})
}

// progressOnly is a drop func for consumeStream: an event decoding as T that
// is mere progress may go when the reader falls behind, never the result.
func progressOnly[T any](progress func(T) bool) func(json.RawMessage) bool {
	return func(b json.RawMessage) bool {
		var v T
		return json.Unmarshal(b, &v) == nil && progress(v)
	}
}
