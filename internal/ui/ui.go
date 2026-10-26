// Package ui is the window: who is nearby, one button to call, one to pick up,
// one to hang up, a chat box, the call log, and the person's own name. Built
// with Fyne, so the same code is a native window on Linux, macOS and Windows.
package ui

import (
	"fmt"
	"image/color"
	"log"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/mmdmcy/belleke/internal/call"
	"github.com/mmdmcy/belleke/internal/identity"
	"github.com/mmdmcy/belleke/internal/store"
	"github.com/mmdmcy/belleke/internal/wire"
)

// Window is the running interface.
type Window struct {
	app     fyne.App
	win     fyne.Window
	engine  *call.Engine
	store   *store.Store
	me      *identity.Identity
	devices *fyne.Container
	status  *widget.Label
	timer   *widget.Label
	chatBox *fyne.Container
	chatIn  *widget.Entry
	callLog *fyne.Container
	hangup  *widget.Button
	mute    *widget.Button
	meterIn *canvas.Rectangle
	meterOu *canvas.Rectangle
	ringing dialog.Dialog
	chatFor string
}

// Run shows the window and blocks until it closes.
func Run(engine *call.Engine, st *store.Store, me *identity.Identity, version string) {
	a := app.NewWithID("com.katteke.belleke")
	a.Settings().SetTheme(theme.DefaultTheme())
	w := a.NewWindow("Belleke")
	ui := &Window{app: a, win: w, engine: engine, store: st, me: me}
	engine.Knock = ui.knock
	w.SetContent(ui.build(version))
	w.Resize(fyne.NewSize(720, 520))
	w.SetMaster()
	go ui.events()
	go ui.tick()
	w.ShowAndRun()
}

func (ui *Window) build(version string) fyne.CanvasObject {
	name := widget.NewEntry()
	name.SetText(ui.store.Settings().Name)
	name.OnChanged = func(s string) {
		if strings.TrimSpace(s) == "" {
			return
		}
		_ = ui.store.SaveSettings(store.Settings{Name: strings.TrimSpace(s)})
	}
	nameRow := container.NewBorder(nil, nil, widget.NewLabel("Your name"), nil, name)

	ui.devices = container.NewVBox()
	ui.status = widget.NewLabel("Not in a call")
	ui.status.TextStyle = fyne.TextStyle{Bold: true}
	ui.timer = widget.NewLabel("")
	ui.hangup = widget.NewButtonWithIcon("Hang up", theme.CancelIcon(), func() { ui.engine.Hangup() })
	ui.hangup.Importance = widget.DangerImportance
	ui.hangup.Disable()
	ui.mute = widget.NewButtonWithIcon("Mute", theme.VolumeMuteIcon(), func() {
		ui.engine.SetMuted(!ui.engine.Muted())
		if ui.engine.Muted() {
			ui.mute.SetText("Unmute")
		} else {
			ui.mute.SetText("Mute")
		}
	})
	ui.mute.Disable()
	ui.meterIn = canvas.NewRectangle(color.NRGBA{R: 60, G: 180, B: 90, A: 255})
	ui.meterOu = canvas.NewRectangle(color.NRGBA{R: 70, G: 130, B: 220, A: 255})
	ui.meterIn.SetMinSize(fyne.NewSize(2, 6))
	ui.meterOu.SetMinSize(fyne.NewSize(2, 6))
	meters := container.NewVBox(
		container.NewHBox(widget.NewLabel("Mic    "), container.NewWithoutLayout(ui.meterIn)),
		container.NewHBox(widget.NewLabel("Sound"), container.NewWithoutLayout(ui.meterOu)),
	)
	callPanel := container.NewVBox(
		container.NewHBox(ui.status, ui.timer, layout.NewSpacer(), ui.mute, ui.hangup),
		meters,
	)

	ui.chatBox = container.NewVBox()
	ui.chatIn = widget.NewEntry()
	ui.chatIn.SetPlaceHolder("Type a message and press Enter")
	ui.chatIn.OnSubmitted = func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		if err := ui.engine.Chat(text); err != nil {
			ui.status.SetText(err.Error())
			return
		}
		ui.chatIn.SetText("")
	}
	chat := container.NewBorder(widget.NewLabelWithStyle("Chat", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), ui.chatIn, nil, nil, container.NewVScroll(ui.chatBox))

	ui.callLog = container.NewVBox()
	logTab := container.NewVScroll(ui.callLog)

	about := widget.NewLabel(fmt.Sprintf("Belleke %s. Calls go straight between the two devices on your network, sealed with keys made on each device. Nothing passes through a server; no account exists.\n\nYour device id: %s\n\nTrust a device once by comparing the six digits both screens show; after that one tap calls it.", version, ui.me.ID()))
	about.Wrapping = fyne.TextWrapWord

	left := container.NewBorder(
		container.NewVBox(nameRow, widget.NewSeparator(), widget.NewLabelWithStyle("Nearby", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})),
		nil, nil, nil,
		container.NewVScroll(ui.devices),
	)
	right := container.NewAppTabs(
		container.NewTabItem("Chat", chat),
		container.NewTabItem("Calls", logTab),
		container.NewTabItem("About", about),
	)
	split := container.NewHSplit(left, right)
	split.SetOffset(0.42)
	ui.refreshDevices()
	ui.refreshLog()
	return container.NewBorder(nil, callPanel, nil, nil, split)
}

func (ui *Window) refreshDevices() {
	ui.devices.Objects = nil
	seen := ui.engine.Devices()
	trusted := ui.store.Peers()
	shown := map[string]bool{}
	for _, s := range seen {
		shown[s.ID] = true
		row := ui.deviceRow(s.Name, s.ID, true)
		ui.devices.Add(row)
	}
	for _, p := range trusted {
		if !shown[p.ID] {
			ui.devices.Add(ui.deviceRow(p.Name, p.ID, false))
		}
	}
	if len(ui.devices.Objects) == 0 {
		hint := widget.NewLabel("Nobody yet. Start Belleke on the other device on the same Wi-Fi or cable; it appears here by itself.")
		hint.Wrapping = fyne.TextWrapWord
		ui.devices.Add(hint)
	}
	ui.devices.Refresh()
}

func (ui *Window) deviceRow(name, id string, online bool) fyne.CanvasObject {
	_, isTrusted := ui.store.Peer(id)
	label := widget.NewLabel(name)
	label.TextStyle = fyne.TextStyle{Bold: true}
	state := "away"
	if online {
		state = "online"
	}
	if !isTrusted {
		state += ", not yet trusted"
	}
	sub := widget.NewLabel(state)
	var action *widget.Button
	switch {
	case !isTrusted:
		action = widget.NewButtonWithIcon("Trust", theme.ConfirmIcon(), func() { ui.trust(name, id) })
	case online:
		action = widget.NewButtonWithIcon("Call", theme.MediaPlayIcon(), func() {
			if err := ui.engine.Call(id); err != nil {
				dialog.ShowError(err, ui.win)
			}
		})
		action.Importance = widget.HighImportance
	default:
		action = widget.NewButton("Call", nil)
		action.Disable()
	}
	more := widget.NewButtonWithIcon("", theme.MoreHorizontalIcon(), func() {
		items := []*fyne.MenuItem{
			fyne.NewMenuItem("Show chat", func() { ui.showChat(id, name) }),
		}
		if isTrusted {
			items = append(items, fyne.NewMenuItem("Forget this device", func() {
				_ = ui.store.Forget(id)
				ui.refreshDevices()
			}))
		}
		widget.ShowPopUpMenuAtPosition(fyne.NewMenu("", items...), ui.win.Canvas(), fyne.CurrentApp().Driver().AbsolutePositionForObject(label))
	})
	return container.NewBorder(nil, nil, nil, container.NewHBox(action, more), container.NewVBox(label, sub))
}

func (ui *Window) trust(name, id string) {
	pub, err := identity.Decode(id)
	if err != nil {
		dialog.ShowError(err, ui.win)
		return
	}
	code := identity.Fingerprint(ui.me.Public, pub)
	text := widget.NewLabel(fmt.Sprintf("Both screens must show the same six digits:\n\n        %s\n\nIf they match, this device is really %s. Trust it on both sides.", code, name))
	text.Wrapping = fyne.TextWrapWord
	dialog.ShowCustomConfirm("Trust "+name+"?", "They match, trust", "Not now", text, func(ok bool) {
		if !ok {
			return
		}
		_ = ui.store.Trust(store.Peer{ID: id, Name: name})
		ui.refreshDevices()
	}, ui.win)
}

// knock runs on the engine's goroutine when an untrusted device calls.
func (ui *Window) knock(id, name string) bool {
	answer := make(chan bool, 1)
	pub, err := identity.Decode(id)
	if err != nil {
		return false
	}
	code := identity.Fingerprint(ui.me.Public, pub)
	fyne.Do(func() {
		text := widget.NewLabel(fmt.Sprintf("%s is calling but is not trusted yet. Trust it if the other screen shows the same six digits:\n\n        %s", name, code))
		text.Wrapping = fyne.TextWrapWord
		dialog.ShowCustomConfirm("Unknown device", "They match, trust", "Refuse", text, func(ok bool) {
			if ok {
				_ = ui.store.Trust(store.Peer{ID: id, Name: name})
				ui.refreshDevices()
			}
			answer <- ok
		}, ui.win)
	})
	select {
	case ok := <-answer:
		return ok
	case <-time.After(18 * time.Second):
		return false
	}
}

func (ui *Window) showChat(id, name string) {
	ui.chatFor = id
	ui.chatBox.Objects = nil
	for _, c := range ui.store.Chats(id) {
		ui.chatBox.Add(ui.chatLine(c, name))
	}
	ui.chatBox.Refresh()
}

func (ui *Window) chatLine(c store.Chat, peer string) fyne.CanvasObject {
	who := peer
	if c.Mine {
		who = "You"
	}
	l := widget.NewLabel(fmt.Sprintf("%s  %s: %s", c.When.Format("15:04"), who, c.Text))
	l.Wrapping = fyne.TextWrapWord
	return l
}

func (ui *Window) refreshLog() {
	ui.callLog.Objects = nil
	calls := ui.store.Calls()
	if len(calls) == 0 {
		ui.callLog.Add(widget.NewLabel("No calls yet."))
	}
	for _, c := range calls {
		dir := "incoming"
		if c.Outgoing {
			dir = "outgoing"
		}
		dur := ""
		if c.Seconds > 0 {
			dur = fmt.Sprintf(", %d:%02d", c.Seconds/60, c.Seconds%60)
		}
		ui.callLog.Add(widget.NewLabel(fmt.Sprintf("%s  %s  %s, %s%s", c.Start.Format("Mon 2 Jan 15:04"), c.PeerName, dir, c.Result, dur)))
	}
	ui.callLog.Refresh()
}

func (ui *Window) events() {
	for ev := range ui.engine.Events {
		ev := ev
		fyne.Do(func() { ui.handle(ev) })
	}
}

func (ui *Window) handle(ev call.Event) {
	switch ev.Kind {
	case "devices":
		ui.refreshDevices()
	case "chat":
		if ui.chatFor != ev.PeerID {
			ui.showChat(ev.PeerID, ev.PeerName)
		} else {
			ui.chatBox.Add(ui.chatLine(store.Chat{When: time.Now(), Mine: false, Text: ev.Text}, ev.PeerName))
			ui.chatBox.Refresh()
		}
		if ui.engine.State() == call.Idle {
			ui.app.SendNotification(fyne.NewNotification(ev.PeerName, ev.Text))
		}
	case "error":
		log.Printf("error: %v", ev.Err)
		if ev.Err != nil {
			ui.status.SetText(ev.Err.Error())
		}
	case "knock":
	case "state":
		ui.state(ev)
	}
}

func (ui *Window) state(ev call.Event) {
	if ui.ringing != nil && ev.State != call.Ringing {
		ui.ringing.Hide()
		ui.ringing = nil
	}
	switch ev.State {
	case call.Idle:
		ui.status.SetText("Not in a call")
		ui.timer.SetText("")
		ui.hangup.Disable()
		ui.mute.Disable()
		ui.refreshLog()
	case call.Finished:
		ui.status.SetText(fmt.Sprintf("Call with %s ended (%s)", ev.PeerName, ev.Text))
		ui.refreshLog()
	case call.Calling:
		ui.status.SetText("Calling " + ev.PeerName + "…")
		ui.hangup.Enable()
	case call.Ringing:
		ui.status.SetText(ev.PeerName + " is calling")
		ui.hangup.Enable()
		ui.app.SendNotification(fyne.NewNotification("Belleke", ev.PeerName+" is calling"))
		ui.ringing = dialog.NewCustomConfirm(ev.PeerName+" is calling", "Pick up", "Decline", widget.NewLabel("Pick up to talk."), func(ok bool) {
			if ok {
				if err := ui.engine.Answer(); err != nil {
					dialog.ShowError(err, ui.win)
				}
			} else {
				ui.engine.Decline()
			}
		}, ui.win)
		ui.ringing.Show()
		ui.win.RequestFocus()
	case call.InCall:
		ui.status.SetText("In a call with " + ev.PeerName)
		ui.hangup.Enable()
		ui.mute.Enable()
		ui.showChat(ev.PeerID, ev.PeerName)
	}
}

func (ui *Window) tick() {
	t := time.NewTicker(250 * time.Millisecond)
	for range t.C {
		fyne.Do(func() {
			if ui.engine.State() == call.InCall {
				d := time.Since(ui.engine.Since()).Round(time.Second)
				ui.timer.SetText(fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60))
			}
			in, out := ui.engine.Levels()
			ui.meterIn.Resize(fyne.NewSize(float32(2+in*200), 6))
			ui.meterOu.Resize(fyne.NewSize(float32(2+out*200), 6))
			if time.Now().Second()%2 == 0 {
				ui.refreshDevices()
			}
		})
	}
}

// Seen re-exports for callers that list devices without the window.
type Seen = wire.Seen
