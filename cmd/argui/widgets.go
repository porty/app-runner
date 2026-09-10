package main

import (
	"fmt"
	"html"
	"strings"
	"unsafe"

	"github.com/mappu/miqt/qt6"
)

type messageKind int

const (
	messageInfo messageKind = iota
	messageWarning
	messageError
	messagePositive
)

// messageWidget is an inline banner in the spirit of KMessageWidget: an
// icon, wrapped text, optional action buttons, and a close button.
type messageWidget struct {
	*qt6.QFrame
	icon    *qt6.QLabel
	text    *qt6.QLabel
	actions *qt6.QHBoxLayout
	close   *qt6.QToolButton
	buttons []*qt6.QPushButton
	onClose func()
}

func newMessageWidget(closable bool) *messageWidget {
	m := &messageWidget{QFrame: qt6.NewQFrame2()}
	m.SetObjectName(*qt6.NewQAnyStringView3("messageWidget"))
	layout := qt6.NewQHBoxLayout(m.QWidget)
	layout.SetContentsMargins(10, 6, 6, 6)
	layout.SetSpacing(8)
	m.icon = qt6.NewQLabel2()
	m.icon.SetFixedWidth(18)
	layout.AddWidget3(m.icon.QWidget, 0, qt6.AlignTop)
	m.text = qt6.NewQLabel2()
	m.text.SetWordWrap(true)
	m.text.SetTextFormat(qt6.RichText)
	m.text.SetTextInteractionFlags(qt6.TextBrowserInteraction)
	m.text.SetOpenExternalLinks(false)
	layout.AddWidget2(m.text.QWidget, 1)
	m.actions = qt6.NewQHBoxLayout2()
	m.actions.SetSpacing(6)
	layout.AddLayout(m.actions.QLayout)
	if closable {
		m.close = qt6.NewQToolButton2()
		m.close.SetAutoRaise(true)
		m.close.SetIcon(themeIcon("dialog-close", qt6.QStyle__SP_DialogCloseButton))
		m.close.SetToolTip("Dismiss")
		m.close.OnClicked(func() {
			m.Hide()
			if m.onClose != nil {
				m.onClose()
			}
		})
		layout.AddWidget3(m.close.QWidget, 0, qt6.AlignTop)
	}
	m.Hide()
	return m
}

func (m *messageWidget) show(kind messageKind, text string) {
	border, background, glyph := colorHighlight, "rgba(61,174,233,0.15)", "i"
	switch kind {
	case messageWarning:
		border, background, glyph = colorNeutral, "rgba(246,116,0,0.15)", "!"
	case messageError:
		border, background, glyph = colorNegative, "rgba(218,68,83,0.15)", "!"
	case messagePositive:
		border, background, glyph = colorPositive, "rgba(39,174,96,0.15)", "✓"
	}
	m.SetStyleSheet(fmt.Sprintf("QFrame#messageWidget { background: %s; border: 1px solid %s; border-radius: 3px; } QLabel { background: transparent; border: none; }", background, border))
	m.icon.SetTextFormat(qt6.RichText)
	m.icon.SetText(fmt.Sprintf(`<span style="color:%s; font-weight:bold">%s</span>`, border, glyph))
	m.text.SetText(text)
	m.Show()
}

// setActions replaces the banner's buttons.
func (m *messageWidget) setActions(actions ...bannerAction) {
	for _, button := range m.buttons {
		m.actions.RemoveWidget(button.QWidget)
		button.Hide()
		button.DeleteLater()
	}
	m.buttons = nil
	for _, action := range actions {
		button := qt6.NewQPushButton3(action.label)
		handler := action.handler
		button.OnClicked(func() { handler() })
		m.actions.AddWidget(button.QWidget)
		m.buttons = append(m.buttons, button)
	}
}

type bannerAction struct {
	label   string
	handler func()
}

func escape(text string) string {
	return html.EscapeString(text)
}

func mutedHTML(text string) string {
	return fmt.Sprintf(`<span style="color:%s">%s</span>`, colorInactive, escape(text))
}

func monoHTML(text string) string {
	return fmt.Sprintf(`<span style="font-family:monospace">%s</span>`, escape(text))
}

func newLabel(text string) *qt6.QLabel {
	label := qt6.NewQLabel3(text)
	label.SetWordWrap(true)
	return label
}

func richLabel(html string) *qt6.QLabel {
	label := qt6.NewQLabel2()
	label.SetTextFormat(qt6.RichText)
	label.SetWordWrap(true)
	label.SetText(html)
	return label
}

func mutedLabel(text string) *qt6.QLabel {
	label := richLabel(mutedHTML(text))
	label.SetWordWrap(true)
	return label
}

// inlineLabel is a rich-text label that never wraps, for toolbars and
// status footers.
func inlineLabel(html string) *qt6.QLabel {
	label := richLabel(html)
	label.SetWordWrap(false)
	return label
}

func headingLabel(text string) *qt6.QLabel {
	label := qt6.NewQLabel3(text)
	font := label.Font()
	font.SetBold(true)
	font.SetPointSize(font.PointSize() + 2)
	label.SetFont(font)
	return label
}

func boldLabel(text string) *qt6.QLabel {
	label := qt6.NewQLabel3(text)
	font := label.Font()
	font.SetBold(true)
	label.SetFont(font)
	return label
}

func sectionLabel(text string) *qt6.QLabel {
	label := richLabel(fmt.Sprintf(`<span style="color:%s; font-size:8pt; font-weight:bold; letter-spacing:1px">%s</span>`, colorInactive, escape(strings.ToUpper(text))))
	return label
}

func horizontalLine() *qt6.QFrame {
	line := qt6.NewQFrame2()
	line.SetFrameShape(qt6.QFrame__HLine)
	line.SetFrameShadow(qt6.QFrame__Sunken)
	return line
}

func verticalLine() *qt6.QFrame {
	line := qt6.NewQFrame2()
	line.SetFrameShape(qt6.QFrame__VLine)
	line.SetFrameShadow(qt6.QFrame__Sunken)
	return line
}

func spacer() *qt6.QWidget {
	widget := qt6.NewQWidget2()
	widget.SetSizePolicy2(qt6.QSizePolicy__Expanding, qt6.QSizePolicy__Preferred)
	return widget
}

// newTable configures a QTreeWidget as a flat details table.
func newTable(columns ...string) *qt6.QTreeWidget {
	table := qt6.NewQTreeWidget2()
	table.SetColumnCount(len(columns))
	table.SetHeaderLabels(columns)
	table.SetRootIsDecorated(false)
	table.SetAlternatingRowColors(true)
	table.SetUniformRowHeights(false)
	table.SetAllColumnsShowFocus(true)
	table.SetSelectionBehavior(qt6.QAbstractItemView__SelectRows)
	table.SetSelectionMode(qt6.QAbstractItemView__SingleSelection)
	table.SetEditTriggers(qt6.QAbstractItemView__NoEditTriggers)
	table.SetVerticalScrollMode(qt6.QAbstractItemView__ScrollPerPixel)
	table.SetTextElideMode(qt6.ElideRight)
	table.Header().SetStretchLastSection(true)
	return table
}

func setItemData(item *qt6.QTreeWidgetItem, value string) {
	item.SetData(0, int(qt6.UserRole), qt6.NewQVariant14(value))
}

func itemData(item *qt6.QTreeWidgetItem) string {
	if item == nil {
		return ""
	}
	return item.Data(0, int(qt6.UserRole)).ToString()
}

func setMuted(item *qt6.QTreeWidgetItem, column int) {
	item.SetForeground(column, qt6.NewQBrush3(color(colorInactive)))
}

func setMono(item *qt6.QTreeWidgetItem, column int) {
	item.SetFont(column, monospaceFont())
}

func setBold(item *qt6.QTreeWidgetItem, column int) {
	font := qt6.NewQFont()
	font.SetBold(true)
	item.SetFont(column, font)
}

// confirm asks a yes/no question with a labelled affirmative button.
func confirm(parent *qt6.QWidget, title, text, acceptLabel string, destructive bool) bool {
	box := qt6.NewQMessageBox(parent)
	box.SetWindowTitle(title)
	box.SetText(text)
	if destructive {
		box.SetIcon(qt6.QMessageBox__Warning)
	} else {
		box.SetIcon(qt6.QMessageBox__Question)
	}
	role := qt6.QMessageBox__AcceptRole
	if destructive {
		role = qt6.QMessageBox__DestructiveRole
	}
	accept := box.AddButton2(acceptLabel, role)
	cancel := box.AddButton2("Cancel", qt6.QMessageBox__RejectRole)
	box.SetDefaultButton(cancel)
	box.SetEscapeButton(cancel.QAbstractButton)
	box.Exec()
	clicked := box.ClickedButton()
	return clicked != nil && clicked.UnsafePointer() == accept.QAbstractButton.UnsafePointer()
}

func showError(parent *qt6.QWidget, title, text string) {
	qt6.QMessageBox_Critical(parent, title, text)
}

func sameWidget(a, b *qt6.QWidget) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.UnsafePointer() == b.UnsafePointer()
}

func pointerOf(widget *qt6.QWidget) unsafe.Pointer {
	if widget == nil {
		return nil
	}
	return widget.UnsafePointer()
}

// passwordEdit is a line edit with an eye toggle to reveal the password.
func passwordEdit() *qt6.QLineEdit {
	edit := qt6.NewQLineEdit2()
	edit.SetEchoMode(qt6.QLineEdit__Password)
	action := edit.AddAction2(themeIcon("view-visible", 0), qt6.QLineEdit__TrailingPosition)
	action.SetCheckable(true)
	action.SetToolTip("Show password")
	if action.Icon().IsNull() {
		action.SetText("👁")
	}
	action.OnToggled(func(checked bool) {
		if checked {
			edit.SetEchoMode(qt6.QLineEdit__Normal)
		} else {
			edit.SetEchoMode(qt6.QLineEdit__Password)
		}
	})
	return edit
}

func fillForm(form *qt6.QFormLayout) {
	form.SetLabelAlignment(qt6.AlignRight | qt6.AlignVCenter)
	form.SetFieldGrowthPolicy(qt6.QFormLayout__ExpandingFieldsGrow)
	form.SetHorizontalSpacing(12)
	form.SetVerticalSpacing(8)
}

func scrollable(content *qt6.QWidget) *qt6.QScrollArea {
	area := qt6.NewQScrollArea2()
	area.SetWidgetResizable(true)
	area.SetFrameShape(qt6.QFrame__NoFrame)
	area.SetWidget(content)
	return area
}

func setKeyboardShortcut(action *qt6.QAction, sequence string) {
	action.SetShortcut(qt6.NewQKeySequence2(sequence))
}

// chipHTML renders a compact tag with a coloured border.
func chipHTML(text, hex string) string {
	return fmt.Sprintf(`<span style="border:1px solid %s; border-radius:3px; padding:0 5px; color:%s">%s</span>`, hex, hex, escape(text))
}
