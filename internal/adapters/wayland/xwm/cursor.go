package xwm

import (
	"bnema.dev/xturbo"
	"bnema.dev/xturbo/render"
	"bnema.dev/xturbo/xproto"
)

// Root cursor: a themed arrow on the root window so the X background does not
// show Xwayland's cross. One-shot at boot; never fatal.

const maxCursorBytes = 1 << 18 // one PutImage request, below the core limit

// bootRootCursor sets the cursor of the root window.
func (x *XWM) bootRootCursor() error {
	if !x.cfg.RootCursor {
		return nil
	}
	img := x.cfg.Cursor
	usable := img.W > 0 && img.H > 0 && img.W <= 0xffff && img.H <= 0xffff &&
		len(img.Pixels) == img.W*img.H*4 && len(img.Pixels) <= maxCursorBytes
	if usable && x.conn.Ext().Info(xturbo.ExtRender).Present {
		b := x.conn.Buf()
		x.expect(render.QueryPictFormats(b), kindPictFormats, 0, 0)
		if err := x.conn.Sync(&x.h); err != nil {
			return err
		}
		if x.bootErr != nil {
			return x.bootErr
		}
		if x.xs.argb != 0 && x.argbCursor(img.W, img.H, img.HotX, img.HotY, img.Pixels) {
			return nil
		}
	}
	x.glyphCursor()
	return nil
}

// onPictFormatsReply remembers the ARGB32 picture format (depth 32, direct,
// alpha mask 0xff at shift 24).
func (x *XWM) onPictFormatsReply(r *xturbo.Reply) {
	v, ok := render.ParseQueryPictFormatsReply(r.Raw)
	if !ok {
		return
	}
	l := v.Formats()
	for i := 0; i < l.Len(); i++ {
		f := l.At(i)
		if f.Type == render.PictTypeDirect && f.Depth == 32 && f.Direct.AlphaMask == 0xff && f.Direct.AlphaShift == 24 &&
			f.Direct.RedMask == 0xff && f.Direct.RedShift == 16 {
			x.xs.argb = uint32(f.Id)
			return
		}
	}
}

func (x *XWM) newIDs(ids []uint32) bool {
	for i := range ids {
		id, err := x.conn.NewXID()
		if err != nil {
			return false
		}
		ids[i] = id
	}
	return true
}

// argbCursor uploads the image and installs it on the root.
func (x *XWM) argbCursor(w, h, hotX, hotY int, px []byte) bool {
	var id [4]uint32 // pixmap, gc, picture, cursor
	if !x.newIDs(id[:]) {
		return false
	}
	b := x.conn.Buf()
	pm, gc, pic, cur := xproto.Pixmap(id[0]), xproto.Gcontext(id[1]), render.Picture(id[2]), xproto.Cursor(id[3])
	xproto.CreatePixmap(b, 32, pm, xproto.Drawable(x.root), uint16(w), uint16(h))
	var gv xproto.CreateGCValues
	xproto.CreateGC(b, gc, xproto.Drawable(pm), &gv)
	xproto.PutImage(b, xproto.ImageFormatZPixmap, xproto.Drawable(pm), gc, uint16(w), uint16(h), 0, 0, 0, 32, px)
	var pv render.CreatePictureValues
	render.CreatePicture(b, pic, xproto.Drawable(pm), render.Pictformat(x.xs.argb), &pv)
	render.CreateCursor(b, cur, pic, uint16(min(max(hotX, 0), w-1)), uint16(min(max(hotY, 0), h-1)))
	var wv xproto.ChangeWindowAttributesValues
	wv.SetCursor(cur)
	xproto.ChangeWindowAttributes(b, xproto.Window(x.root), &wv)
	render.FreePicture(b, pic)
	xproto.FreeGC(b, gc)
	xproto.FreePixmap(b, pm)
	xproto.FreeCursor(b, cur)
	return true
}

// glyphCursor installs the "left_ptr" glyph of the X cursor font (glyph 68,
// mask 69).
func (x *XWM) glyphCursor() {
	var id [2]uint32 // font, cursor
	if !x.newIDs(id[:]) {
		return
	}
	b := x.conn.Buf()
	font, cur := xproto.Font(id[0]), xproto.Cursor(id[1])
	xproto.OpenFont(b, font, "cursor")
	xproto.CreateGlyphCursor(b, cur, font, font, 68, 69, 0, 0, 0, 0xffff, 0xffff, 0xffff)
	var wv xproto.ChangeWindowAttributesValues
	wv.SetCursor(cur)
	xproto.ChangeWindowAttributes(b, xproto.Window(x.root), &wv)
	xproto.CloseFont(b, font)
	xproto.FreeCursor(b, cur)
}
