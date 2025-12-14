package main

import (
	"errors"
	"fmt"
	"hash/fnv"
	"image/color"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type GUI struct {
	App  fyne.App
	Win  fyne.Window
	Node *P2PNode

	// UI Components
	ChatContainer *fyne.Container
	MsgInput      *widget.Entry
	ChannelList   *widget.List
	FriendList    *widget.List
	NickLabel     *widget.Label
	
	// State
	ActiveChannel string
}

func NewGUI(node *P2PNode) *GUI {
	a := app.New()
	w := a.NewWindow("P2P Chat - " + node.Nick)
	w.Resize(fyne.NewSize(1000, 700))

	gui := &GUI{
		App:           a,
		Win:           w,
		Node:          node,
		ActiveChannel: "global-chat",
	}

	gui.setupUI()
	return gui
}

func (g *GUI) Run() {
	go g.updateLoop()
	
	// Check for nickname
	if strings.HasPrefix(g.Node.Nick, "User-") {
		g.showNicknameDialog()
	}
	
	g.Win.ShowAndRun()
}

func (g *GUI) showNicknameDialog() {
	entry := widget.NewEntry()
	entry.PlaceHolder = "Enter Nickname (3-20 chars)"
	
	// Validation Label
	errorLabel := widget.NewLabel("")
	errorLabel.Hide()
	
	content := container.NewVBox(
		widget.NewLabel("Please set a nickname to identify yourself:"),
		entry,
		errorLabel,
	)
	
	d := dialog.NewCustomConfirm("Set Nickname", "Set", "Exit", container.NewPadded(content), func(ok bool) {
		if !ok {
			g.Win.Close()
			return
		}
		
		nick := strings.TrimSpace(entry.Text)
		if len(nick) < 3 || len(nick) > 20 {
			// Re-show dialog with error
			// Since we can't easily keep this one open, we just show it again?
			// Or better, we don't close if invalid? Fyne dialogs close on callback.
			// We have to re-open.
			dialog.ShowError(fmt.Errorf("Nickname must be between 3 and 20 characters"), g.Win)
			g.showNicknameDialog()
			return
		}
		
		g.Node.Mutex.Lock()
		g.Node.Nick = nick
		g.Node.Mutex.Unlock()
		g.Node.SaveData()
		
		g.Win.SetTitle("P2P Chat - " + nick)
		if g.NickLabel != nil {
			g.NickLabel.SetText("Nick: " + nick)
		}
	}, g.Win)
	
	d.Resize(fyne.NewSize(400, 200))
	d.Show()
}

func (g *GUI) setupUI() {
	// --- Sidebar ---
	
	// Profile Section
	profileLabel := widget.NewLabelWithStyle("Profile", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	
	// Nickname Row
	g.NickLabel = widget.NewLabel("Nick: " + g.Node.Nick)
	editNickBtn := widget.NewButtonWithIcon("", theme.DocumentCreateIcon(), func() {
		g.showNicknameDialog()
	})
	nickRow := container.NewBorder(nil, nil, nil, editNickBtn, g.NickLabel)
	
	// UUID Row
	uuidEntry := widget.NewEntry()
	uuidEntry.SetText(g.Node.StableID)
	uuidEntry.Disable() // Read-only copyable
	copyUUIDBtn := widget.NewButtonWithIcon("", theme.ContentCopyIcon(), func() {
		g.Win.Clipboard().SetContent(g.Node.StableID)
		dialog.ShowInformation("Copied", "UUID copied to clipboard", g.Win)
	})
	uuidRow := container.NewBorder(nil, nil, nil, copyUUIDBtn, uuidEntry)
	
	profileBox := container.NewVBox(profileLabel, nickRow, uuidRow)

	// Channels Section
	channelLabel := widget.NewLabelWithStyle("Channels", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	g.ChannelList = widget.NewList(
		func() int {
			g.Node.Mutex.RLock()
			defer g.Node.Mutex.RUnlock()
			return len(g.Node.Channels)
		},
		func() fyne.CanvasObject {
			// Use Border layout: Label in Center, Trash Button on Right
			label := widget.NewLabel("Channel Name")
			btn := widget.NewButtonWithIcon("", theme.DeleteIcon(), nil)
			return container.NewBorder(nil, nil, nil, btn, label)
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			g.Node.Mutex.RLock()
			
			// Sort keys to ensure stable order
			keys := make([]string, 0, len(g.Node.Channels))
			for k := range g.Node.Channels {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			
			var chID string
			var chName string
			var unread int
			
			if i < len(keys) {
				chID = keys[i]
				ch := g.Node.Channels[chID]
				chName = ch.Name
				unread = ch.Unread
			}
			g.Node.Mutex.RUnlock()
			
			if chID == "" {
				return
			}

			// Find components
			c := o.(*fyne.Container)
			var label *widget.Label
			var btn *widget.Button
			for _, obj := range c.Objects {
				if l, ok := obj.(*widget.Label); ok {
					label = l
				}
				if b, ok := obj.(*widget.Button); ok {
					btn = b
				}
			}
			
			// Update Label
			txt := chName
			if unread > 0 {
				txt = fmt.Sprintf("%s (%d)", txt, unread)
			}
			label.SetText(txt)
			if chID == g.ActiveChannel {
				label.TextStyle = fyne.TextStyle{Bold: true}
			} else {
				label.TextStyle = fyne.TextStyle{}
			}
			
			// Update Button
			if chID == "global-chat" || chID == "local-chat" {
				btn.Disable()
			} else {
				btn.Enable()
				btn.OnTapped = func() {
					dialog.ShowConfirm("Leave Channel", "Are you sure you want to leave "+chName+"?", func(ok bool) {
						if ok {
							g.Node.LeaveChannel(chID)
							g.ChannelList.Refresh()
							if g.ActiveChannel == chID {
								g.ActiveChannel = "global-chat"
								g.refreshChat()
							}
						}
					}, g.Win)
				}
			}
		},
	)
	g.ChannelList.OnSelected = func(id widget.ListItemID) {
		g.Node.Mutex.RLock()
		keys := make([]string, 0, len(g.Node.Channels))
		for k := range g.Node.Channels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		g.Node.Mutex.RUnlock()

		if id < len(keys) {
			g.ActiveChannel = keys[id]
			g.refreshChat()
			g.ChannelList.Refresh() // To update bold style
		}
	}

	addChannelBtn := widget.NewButtonWithIcon("Join/Create", theme.ContentAddIcon(), func() {
		g.showAddChannelDialog()
	})

	// Friends Section
	friendLabel := widget.NewLabelWithStyle("Friends", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	g.FriendList = widget.NewList(
		func() int {
			g.Node.Mutex.RLock()
			defer g.Node.Mutex.RUnlock()
			return len(g.Node.Friends)
		},
		func() fyne.CanvasObject {
			// Use Border layout: Label in Center, Trash Button on Right
			label := widget.NewLabel("Friend Name")
			btn := widget.NewButtonWithIcon("", theme.DeleteIcon(), nil)
			return container.NewBorder(nil, nil, nil, btn, label)
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			g.Node.Mutex.RLock()
			
			keys := make([]string, 0, len(g.Node.Friends))
			for k := range g.Node.Friends {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			
			var fID string
			var fNick string
			
			if i < len(keys) {
				fID = keys[i]
				friend := g.Node.Friends[fID]
				fNick = friend.Nick
			}
			g.Node.Mutex.RUnlock()
			
			if fID == "" {
				return
			}
			
			// Find components
			c := o.(*fyne.Container)
			var label *widget.Label
			var btn *widget.Button
			for _, obj := range c.Objects {
				if l, ok := obj.(*widget.Label); ok {
					label = l
				}
				if b, ok := obj.(*widget.Button); ok {
					btn = b
				}
			}
			
			label.SetText(fmt.Sprintf("%s (%s)", fNick, fID[:8]))
			
			btn.OnTapped = func() {
				dialog.ShowConfirm("Remove Friend", "Are you sure you want to remove "+fNick+"?", func(ok bool) {
					if ok {
						g.Node.RemoveFriend(fID)
						g.FriendList.Refresh()
					}
				}, g.Win)
			}
		},
	)
	
	g.FriendList.OnSelected = func(id widget.ListItemID) {
		g.Node.Mutex.RLock()
		keys := make([]string, 0, len(g.Node.Friends))
		for k := range g.Node.Friends {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		friendID := keys[id]
		g.Node.Mutex.RUnlock()

		// Join DM
		if err := g.Node.JoinDM(friendID); err != nil {
			dialog.ShowError(err, g.Win)
			return
		}
		
		// Switch to DM channel
		dmID := g.Node.GetDMChannelID(friendID)
		g.ActiveChannel = dmID
		g.refreshChat()
		g.ChannelList.Refresh()
	}

	addFriendBtn := widget.NewButtonWithIcon("Add Friend", theme.ContentAddIcon(), func() {
		g.showAddFriendDialog()
	})

	sidebar := container.NewBorder(
		profileBox, 
		container.NewVBox(addChannelBtn, addFriendBtn), 
		nil, nil,
		container.NewVSplit(
			container.NewBorder(channelLabel, nil, nil, nil, g.ChannelList),
			container.NewBorder(friendLabel, nil, nil, nil, g.FriendList),
		),
	)

	// --- Main Chat Area ---
	g.ChatContainer = container.NewVBox()
	scroll := container.NewScroll(g.ChatContainer)

	g.MsgInput = widget.NewEntry()
	g.MsgInput.PlaceHolder = "Type a message..."
	g.MsgInput.OnSubmitted = func(s string) {
		if s == "" {
			return
		}
		if err := g.Node.SendMessage(g.ActiveChannel, s); err != nil {
			dialog.ShowError(err, g.Win)
		} else {
			// Optimistic update
			g.Node.Mutex.Lock()
			ch := g.Node.Channels[g.ActiveChannel]
			ch.Messages = append(ch.Messages, ChatMessage{
				SenderID:   g.Node.StableID, // Assuming stable for now
				SenderName: g.Node.Nick,
				Content:    s,
				Timestamp:  time.Now().Unix(),
			})
			g.Node.Mutex.Unlock()
			g.refreshChat()
			g.MsgInput.SetText("")
		}
	}

	sendBtn := widget.NewButtonWithIcon("", theme.MailSendIcon(), func() {
		g.MsgInput.OnSubmitted(g.MsgInput.Text)
	})

	inputBar := container.NewBorder(nil, nil, nil, sendBtn, g.MsgInput)

	mainContent := container.NewBorder(nil, inputBar, nil, nil, scroll)

	// Split View
	split := container.NewHSplit(sidebar, mainContent)
	split.SetOffset(0.3)

	g.Win.SetContent(split)
}

func (g *GUI) refreshChat() {
	g.ChatContainer.Objects = nil

	g.Node.Mutex.RLock()
	ch, exists := g.Node.Channels[g.ActiveChannel]
	g.Node.Mutex.RUnlock()

	if !exists {
		g.ChatContainer.Add(widget.NewLabel("Channel not found"))
		g.ChatContainer.Refresh()
		return
	}

	// Mark as read
	g.Node.Mutex.Lock()
	ch.Unread = 0
	g.Node.Mutex.Unlock()

	for _, msg := range ch.Messages {
		isMe := (msg.SenderID == g.Node.StableID || msg.SenderID == g.Node.EphemeralID)
		g.ChatContainer.Add(createMessageBubble(msg, isMe, g.ActiveChannel, g.Win))
	}
	
	g.ChatContainer.Refresh()
}

// --- Custom Widgets ---

// UsernameWidget is a label that supports right-click to copy UUID
type UsernameWidget struct {
	widget.BaseWidget
	Name   string
	UUID   string
	Color  color.Color
	BgColor color.Color
	Win    fyne.Window
}

func NewUsernameWidget(name, uuid string, win fyne.Window) *UsernameWidget {
	c := generateColor(name)
	bg := generateBackgroundColor(c)
	u := &UsernameWidget{
		Name:    name,
		UUID:    uuid,
		Color:   c,
		BgColor: bg,
		Win:     win,
	}
	u.ExtendBaseWidget(u)
	return u
}

func (u *UsernameWidget) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(u.BgColor)
	bg.CornerRadius = 4
	
	text := canvas.NewText(u.Name, u.Color)
	text.TextSize = 14
	text.TextStyle = fyne.TextStyle{Bold: true}
	
	return &usernameRenderer{
		widget: u,
		bg:     bg,
		text:   text,
		objects: []fyne.CanvasObject{bg, text},
	}
}

func (u *UsernameWidget) TappedSecondary(pe *fyne.PointEvent) {
	u.Win.Clipboard().SetContent(u.UUID)
	dialog.ShowInformation("Copied", "User UUID copied to clipboard:\n"+u.UUID, u.Win)
}

type usernameRenderer struct {
	widget  *UsernameWidget
	bg      *canvas.Rectangle
	text    *canvas.Text
	objects []fyne.CanvasObject
}

func (r *usernameRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	r.text.Resize(size)
	// Center text? Or padding?
	// Let's add small padding
	padding := float32(4)
	r.text.Move(fyne.NewPos(padding, padding))
	r.bg.Resize(fyne.NewSize(r.text.MinSize().Width + padding*2, r.text.MinSize().Height + padding*2))
}

func (r *usernameRenderer) MinSize() fyne.Size {
	padding := float32(4)
	s := r.text.MinSize()
	return fyne.NewSize(s.Width + padding*2, s.Height + padding*2)
}

func (r *usernameRenderer) Refresh() {
	r.text.Text = r.widget.Name
	r.text.Color = r.widget.Color
	r.bg.FillColor = r.widget.BgColor
	r.bg.Refresh()
	r.text.Refresh()
}

func (r *usernameRenderer) Objects() []fyne.CanvasObject {
	return r.objects
}

func (r *usernameRenderer) Destroy() {}

// ---

func createMessageBubble(msg ChatMessage, isMe bool, channelID string, win fyne.Window) fyne.CanvasObject {
	ts := time.Unix(msg.Timestamp, 0).Format("15:04")
	
	// Username Widget
	// Display: Nickname (UUID-Short)
	// If Local Chat: Display Ephemeral ID
	// If Me: Append (you)
	
	displayID := msg.SenderID
	if channelID == "local-chat" {
		// In local chat, SenderID IS the ephemeral ID.
		// But we want to make sure we show it clearly.
		// It is already in msg.SenderID.
	}
	
	// Show Full ID
	displayName := fmt.Sprintf("%s (%s)", msg.SenderName, displayID)
	if isMe {
		displayName += " (you)"
	}
	
	userWidget := NewUsernameWidget(displayName, msg.SenderID, win)
	
	timeLabel := canvas.NewText(ts, color.Gray{Y: 120})
	timeLabel.TextSize = 12

	// Header: [Username] [Time]
	header := container.NewHBox(userWidget, timeLabel)
	
	// Content
	contentLabel := widget.NewLabel(msg.Content)
	contentLabel.Wrapping = fyne.TextWrapWord
	
	// Bubble Background
	bubbleColor := color.NRGBA{R: 40, G: 40, B: 40, A: 255} // Dark Gray
	if isMe {
		bubbleColor = color.NRGBA{R: 30, G: 60, B: 30, A: 255} // Dark Greenish
	}
	
	bg := canvas.NewRectangle(bubbleColor)
	bg.CornerRadius = 8
	
	// Stack content over background
	// We need a VBox inside the stack
	innerBox := container.NewVBox(header, contentLabel)
	bubble := container.NewStack(bg, container.NewPadded(innerBox))
	
	// Align Left always
	return container.NewBorder(nil, nil, nil, layout.NewSpacer(), bubble)
}

func generateColor(s string) color.Color {
	h := fnv.New32a()
	h.Write([]byte(s))
	val := h.Sum32()
	
	// Generate a bright pastel-ish color
	// Ensure high value/saturation
	r := uint8((val >> 16) & 0xFF)
	g := uint8((val >> 8) & 0xFF)
	b := uint8(val & 0xFF)
	
	// Boost brightness
	if r < 100 { r += 100 }
	if g < 100 { g += 100 }
	if b < 100 { b += 100 }
	
	return color.NRGBA{R: r, G: g, B: b, A: 255}
}

func generateBackgroundColor(c color.Color) color.Color {
	r, g, b, _ := c.RGBA()
	// Make it very dark/transparent version of the color
	// Actually user asked for: "if the color is dark, it should render a lighter background"
	// But we generated bright text colors.
	// Let's make the background a dark version of the text color with some opacity
	
	return color.NRGBA{
		R: uint8(r >> 8) / 4,
		G: uint8(g >> 8) / 4,
		B: uint8(b >> 8) / 4,
		A: 100,
	}
}

func (g *GUI) updateLoop() {
	for {
		select {
		case id := <-g.Node.ChannelUpdates:
			if id == g.ActiveChannel {
				g.refreshChat()
			}
			g.ChannelList.Refresh()
		}
	}
}

func (g *GUI) showAddChannelDialog() {
	idEntry := widget.NewEntry()
	idEntry.PlaceHolder = "Channel UUID (Leave empty to generate)"
	
	passEntry := widget.NewEntry()
	passEntry.PlaceHolder = "Password (Optional)"
	
	content := container.NewVBox(
		widget.NewLabel("Channel ID:"),
		idEntry,
		widget.NewLabel("Password:"),
		passEntry,
	)
	
	d := dialog.NewCustomConfirm("Join / Create Channel", "Join", "Cancel", container.NewPadded(content), func(ok bool) {
		if ok {
			id := idEntry.Text
			if id == "" {
				id = uuid.New().String()
			}
			if err := g.Node.JoinChannel(id, passEntry.Text, false); err != nil {
				dialog.ShowError(err, g.Win)
			} else {
				g.ChannelList.Refresh()
			}
		}
	}, g.Win)
	
	d.Resize(fyne.NewSize(400, 250))
	d.Show()
}

func (g *GUI) showAddFriendDialog() {
	idEntry := widget.NewEntry()
	idEntry.PlaceHolder = "Friend UUID"
	
	nickEntry := widget.NewEntry()
	nickEntry.PlaceHolder = "Nickname"
	
	content := container.NewVBox(
		widget.NewLabel("Friend UUID:"),
		idEntry,
		widget.NewLabel("Nickname:"),
		nickEntry,
	)
	
	d := dialog.NewCustomConfirm("Add Friend", "Add", "Cancel", container.NewPadded(content), func(ok bool) {
		if ok {
			idText := strings.TrimSpace(idEntry.Text)
			nickText := strings.TrimSpace(nickEntry.Text)
			
			if idText != "" {
				// Validate UUID
				if _, err := uuid.Parse(idText); err != nil {
					dialog.ShowError(errors.New("Invalid UUID: Must be a valid 36-character UUID"), g.Win)
					return
				}
				
				if err := g.Node.AddFriend(idText, nickText); err != nil {
					dialog.ShowError(err, g.Win)
				} else {
					g.FriendList.Refresh()
				}
			}
		}
	}, g.Win)
	
	d.Resize(fyne.NewSize(400, 250))
	d.Show()
}
