package app

import (
	"context"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
)

// senderAvatar is the sender's avatar a message shows, as far as it may: an
// image loads only as core allows the message's images, the user's 加载图片
// (allow) or the address books' card. All of it lives on the UI thread.
type senderAvatar struct {
	avatar        ui.ImageSource
	avatarSource  string // where avatar came from: "carddav" for an address book's card
	avatarRun     latest // reading it
	avatarDone    bool   // read since it was last forgotten
	avatarAllow   bool   // the user's 加载图片 asked for it
	avatarBlocked bool   // its images are blocked: the notice offers it
}

// stopAvatar drops the avatar's read in flight, if any.
func (v *senderAvatar) stopAvatar() { v.avatarRun.Stop() }

// forgetAvatar drops the avatar, shown or under way, to be read again.
func (v *senderAvatar) forgetAvatar(blocked bool) {
	v.stopAvatar()
	v.avatar, v.avatarSource, v.avatarDone, v.avatarBlocked = nil, "", false, blocked
}

// avatarPending is whether the avatar is neither read nor being read.
func (v *senderAvatar) avatarPending() bool { return !v.avatarDone && !v.avatarRun.loading }

// avatarChange is what changed outside the reader about which avatars show.
type avatarChange int

const (
	// trustChanged: what lets images load changed (the trusted senders, the
	// remote images setting, an account gone): the user's 加载图片 goes too.
	trustChanged avatarChange = iota
	// settingsChanged: the settings changed elsewhere; 加载图片 stays.
	settingsChanged
	// settingsSettled: a local settings change was saved or undone, or a load
	// read the settings: what loadAvatar held while one was unsaved loads.
	settingsSettled
	// contactsChanged: the address books' avatars changed.
	contactsChanged
)

// avatarsChanged forgets the avatars why touches and reads again what the
// open messages show: the one way in from outside the reader.
func (r *reader) avatarsChanged(why avatarChange) {
	switch why {
	case trustChanged:
		r.forgetAvatars(true)
	case settingsChanged:
		r.forgetAvatars(false)
	case contactsChanged:
		for _, b := range r.bodies {
			if !b.avatarAllow || b.avatar == nil || b.avatarSource == "carddav" {
				b.forgetAvatar(!b.avatarAllow)
			}
		}
	}
	for id, b := range r.bodies {
		if r.open[id] && b.ready && b.avatarPending() {
			r.loadAvatar(id, b.avatarAllow)
		}
	}
}

// forgetAvatars forgets the avatars shown and under way, those the user
// allowed only when revoke is.
func (r *reader) forgetAvatars(revoke bool) {
	for _, b := range r.bodies {
		if b.avatarAllow && !revoke {
			continue
		}
		b.avatarAllow = false
		b.forgetAvatar(true)
	}
}

// loadAvatar reads message id's avatar, manual for the user's 加载图片, and
// converts its image, off the UI thread. A read again drops the one before.
func (r *reader) loadAvatar(id int64, manual bool) {
	b := r.bodies[id]
	if b == nil || !b.ready {
		return
	}
	b.stopAvatar()
	a, s, get := r.p.a, r.session, r.getAvatar
	b.avatarAllow, b.avatarDone = manual, false
	if !manual && a.settingsVer != a.settingsSavedVer {
		b.avatar, b.avatarSource, b.avatarBlocked = nil, "", true
		return
	}
	var image ui.ImageSource
	var source string
	var trusted, carddav bool
	b.avatarRun.Run(a, func(ctx context.Context) (err error) {
		trusted, err = a.svc.ImagesAllowed(ctx, id)
		if err != nil {
			return err
		}
		if !manual && !trusted {
			books, bErr := a.svc.ListAddressBooks(ctx)
			if bErr != nil || !slices.ContainsFunc(books, func(ab core.AddressBook) bool {
				return slices.ContainsFunc(ab.Collections, func(col core.AddressBookCollection) bool {
					return col.Enabled && col.CardCount > 0
				})
			}) {
				return bErr
			}
		}
		avatar, err := get(ctx, id, manual)
		if err != nil || len(avatar.Data) == 0 {
			return err
		}
		source = avatar.Source
		carddav = source == "carddav"
		if !manual && !trusted && !carddav {
			return nil
		}
		image, err = avatarImage(avatar)
		if err == nil && !manual && !carddav {
			trusted, err = a.svc.ImagesAllowed(ctx, id)
			if !trusted {
				image = nil
			}
		}
		if err == nil {
			err = ctx.Err()
		}
		return err
	}, func(err error) {
		if !r.has(s) {
			return
		}
		b.avatarDone = true
		b.avatarBlocked = !trusted && !manual && (err != nil || !carddav || image == nil)
		if err == nil && image != nil {
			b.avatar, b.avatarSource = image, source
		} else {
			b.avatar, b.avatarSource = nil, ""
		}
	})
}
