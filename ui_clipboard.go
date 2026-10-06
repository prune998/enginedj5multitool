package main

import (
	"runtime"
	"strings"

	. "go.hasen.dev/shirei"
	. "go.hasen.dev/shirei/widgets"
)

// ui_clipboard.go: copy & paste for all UI text. Right-clicking a label
// offers "Copy" (the whole text — shirei labels have no per-character
// selection), and right-clicking an editable field offers Copy / Paste;
// Cmd+C / Cmd+V keep working inside fields through the text input itself.
//
// The menu is a frame popup (same mechanism as widgets.Toast) driven by
// package-level state: any widget can arm it while the frame is built, and
// the popup drain renders it on top of everything. One menu at a time.

// clipItem is one row of the clipboard context menu.
type clipItem struct {
	icon  IconGlyph
	label string
	run   func() // invoked on click; the menu is closed first
}

// clipMenuState is the open context menu. armed is false on the frame that
// opened the menu: the rows ignore hover and clicks on that frame, so the
// triggering right-click cannot activate a row that happens to sit under
// the pointer (the release would otherwise fire it).
type clipMenuState struct {
	open   bool
	armed  bool
	pos    Vec2        // where the menu opens (the arming click point)
	size   float32     // item font size (scaled at open time)
	bg     Vec4        // panel face
	border Vec4        // panel hairline
	text   Vec4        // item ink
	dim    Vec4        // item icon ink
	hover  Vec4        // hovered row face
	items  []clipItem  // rows, in display order
	menuID ContainerId // the panel, for outside-click detection
}

// clipMenu is the single-window menu state.
var clipMenu clipMenuState

func init() {
	RegisterFramePopup(clipMenuFrame)
}

// clipRightClicked reports a context-menu click on the current container:
// a secondary-button click, plus Ctrl+click on macOS where AppKit routes it
// like a right-click.
func clipRightClicked() bool {
	fi := GetFrameInput()
	if fi.Mouse != MouseClick || !IsHovered() {
		return false
	}
	if GetInputState().MouseButton == MouseSecondary {
		return true
	}
	return runtime.GOOS == "darwin" &&
		GetInputState().MouseButton == MousePrimary && GetInputState().Modifiers&ModCtrl != 0
}

// copyText places text on the clipboard and confirms with a toast whose body
// previews what was copied (whitespace flattened, clipped to one line).
func copyText(text string) {
	RequestTextCopy(text)
	preview := []rune(text)
	if len(preview) > 100 {
		preview = preview[:100]
	}
	body := strings.Join(strings.Fields(string(preview)), " ")
	if body == "" {
		body = "Text placed on the clipboard."
	}
	Toast(SymITick, "Copied", body)
}

// clipTextMenu arms the Copy menu for a piece of static text. Call it right
// after rendering the text inside its own container (see App.L) so the hover
// test covers exactly the text's rect.
func (a *App) clipTextMenu(text string) {
	if text == "" || !clipRightClicked() {
		return
	}
	a.openClipMenu([]clipItem{{
		icon:  SymCopy,
		label: "Copy",
		run:   func() { copyText(text) },
	}})
}

// clipFieldMenu arms the Copy/Paste menu for an editable field. fieldID is
// the field's focusable container: the Paste row focuses it before
// requesting the clipboard read, so the arriving text lands in this field.
// Masked fields never offer Copy (secrets stay off the clipboard).
func (a *App) clipFieldMenu(fieldID ContainerId, buf *string, masked bool) {
	if !clipRightClicked() {
		return
	}
	var items []clipItem
	if !masked && *buf != "" {
		items = append(items, clipItem{
			icon:  SymCopy,
			label: "Copy",
			run:   func() { copyText(*buf) }, // read at click time: current content
		})
	}
	items = append(items, clipItem{
		icon:  TypClipboard,
		label: "Paste",
		run: func() {
			FocusImmediateOn(fieldID)
			RequestPaste()
		},
	})
	a.openClipMenu(items)
}

// openClipMenu arms the menu with items at the current mouse point, styled
// from the app palette so it matches the active theme.
func (a *App) openClipMenu(items []clipItem) {
	if len(items) == 0 {
		return
	}
	p := a.pal()
	clipMenu.open, clipMenu.armed = true, false
	clipMenu.pos = GetInputState().MousePoint
	clipMenu.size = a.fs(12)
	clipMenu.bg, clipMenu.border = p.bgPanel, p.inputBorder
	clipMenu.text, clipMenu.dim, clipMenu.hover = p.text, p.textDim, p.rowHover
	clipMenu.items = items
	RequestNextFrame()
}

// clipPopupShadow is the floating-surface drop shadow (mirrors the widgets
// package's popup treatment: subtle lift, not a heavy frame).
func clipPopupShadow(a *AttrSet) {
	a.Shadow.Blur = 6
	a.Shadow.Alpha = 0.12
	a.Shadow.Offset[1] = 2
}

// clipMenuFrame runs at the start of every popup drain (RegisterFramePopup).
// An outside click closes the menu; otherwise it renders via Popup. The
// frame that opened the menu is exempt — the right-click that armed it is
// still in flight, and re-arming on another label must win over closing.
func clipMenuFrame() {
	if !clipMenu.open {
		return
	}
	if clipMenu.armed && GetFrameInput().Mouse == MouseClick && !IdIsHovered(clipMenu.menuID) {
		clipMenu.open = false
		return
	}
	Popup(clipMenuRender)
}

// clipMenuRender draws the menu panel: a floating card at the arming click
// point, clamped to stay inside the window (the same positioning idea as
// the widgets menu, anchored to the pointer instead of a trigger button).
// Rows highlight on hover and run their action on click.
func clipMenuRender() {
	const sp = float32(4)
	ContainerWithKey("clip-menu", Attrs(
		MinWidth(140), MaxWidth(420), MaxHeight(GetHost().WindowSize[1]-8),
		Corners(6), Pad2(sp, sp), Gap(2), Clip, NoAnimate,
		BackgroundVec(clipMenu.bg),
		BorderWidth(1), BorderColorVec(clipMenu.border),
		clipPopupShadow,
	), func() {
		// Float at the click point, clamped to the window. On the first
		// frame the size is not resolved yet; the menu self-corrects on the
		// next pass (same as the widgets menu positioning).
		pos := clipMenu.pos
		size := GetResolvedSize()
		win := GetHost().WindowSize
		if pos[0]+size[0] > win[0]-sp {
			pos[0] = win[0] - size[0] - sp
		}
		if pos[1]+size[1] > win[1]-sp {
			pos[1] = win[1] - size[1] - sp
		}
		pos[0] = max(pos[0], 0)
		pos[1] = max(pos[1], 0)
		ModAttrs(FloatVec(pos))
		clipMenu.menuID = CurrentId()

		NextAccessRole("menu")
		NextAccessName("clip-menu")
		AssignAccess()

		for _, it := range clipMenu.items {
			item := it
			Container(Attrs(Row, CrossMid, Gap(8), Pad2(6, 4), Corners(4)), func() {
				NextAccessRole("menuitem")
				NextAccessName("clip-item-" + strings.ToLower(item.label))
				AssignAccess()
				if clipMenu.armed { // ignore the pointer on the opening frame
					if IsHovered() {
						ModAttrs(BackgroundVec(clipMenu.hover))
					}
					if PressAction() {
						clipMenu.open = false
						item.run()
					}
				}
				Icon(item.icon, FontSize(clipMenu.size+1), TextColorVec(clipMenu.dim))
				Label(item.label, FontSize(clipMenu.size), TextColorVec(clipMenu.text))
			})
		}
		clipMenu.armed = true
	})
}
