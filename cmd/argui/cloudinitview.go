package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mappu/miqt/qt6"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
)

// codeEditor is a QPlainTextEdit with a line-number gutter.
type codeEditor struct {
	*qt6.QPlainTextEdit
	gutter *qt6.QWidget
}

func newCodeEditor() *codeEditor {
	e := &codeEditor{QPlainTextEdit: newYAMLEditor()}
	e.gutter = qt6.NewQWidget(e.QWidget)
	e.gutter.OnPaintEvent(func(super func(event *qt6.QPaintEvent), event *qt6.QPaintEvent) {
		e.paintGutter(event)
	})
	e.OnBlockCountChanged(func(int) { e.updateGutterWidth() })
	e.OnUpdateRequest(func(*qt6.QRect, int) { e.gutter.Update() })
	e.OnResizeEvent(func(super func(event *qt6.QResizeEvent), event *qt6.QResizeEvent) {
		super(event)
		e.placeGutter()
	})
	e.updateGutterWidth()
	return e
}

func (e *codeEditor) gutterWidth() int {
	digits := len(fmt.Sprint(max(e.BlockCount(), 1)))
	return 12 + e.FontMetrics().HorizontalAdvance("9")*max(digits, 2)
}

func (e *codeEditor) updateGutterWidth() {
	e.SetViewportMargins(e.gutterWidth(), 0, 0, 0)
	e.placeGutter()
}

func (e *codeEditor) placeGutter() {
	rect := e.ContentsRect()
	e.gutter.SetGeometry(rect.Left(), rect.Top(), e.gutterWidth(), rect.Height())
}

func (e *codeEditor) paintGutter(event *qt6.QPaintEvent) {
	painter := qt6.NewQPainter2(e.gutter.QPaintDevice)
	defer painter.End()
	painter.FillRect6(event.Rect(), e.Palette().Color(qt6.QPalette__Active, qt6.QPalette__AlternateBase))
	painter.SetPen(color(colorInactive))
	painter.SetFont(e.Font())
	block := e.FirstVisibleBlock()
	offset := e.ContentOffset()
	height := e.FontMetrics().Height()
	width := e.gutterWidth() - 6
	for block.IsValid() {
		geometry := e.BlockBoundingGeometry(&block)
		top := int(geometry.Top() + offset.Y())
		if top > event.Rect().Bottom() {
			break
		}
		if block.IsVisible() && top+int(geometry.Height()) >= event.Rect().Top() {
			painter.DrawText7(0, top, width, height, int(qt6.AlignRight), fmt.Sprint(block.BlockNumber()+1))
		}
		next := block.Next()
		block = *next
	}
}

// cloudInitView is the Kate-style template editor: a filterable list of
// templates on the left and a YAML editor with a status footer on the right.
type cloudInitView struct {
	mw     *mainWindow
	root   *qt6.QWidget
	tb     *qt6.QToolBar
	filter *qt6.QLineEdit
	list   *qt6.QListWidget
	name   *qt6.QLineEdit
	editor *codeEditor

	verdictLabel  *qt6.QLabel
	usedLabel     *qt6.QLabel
	positionLabel *qt6.QLabel
	errors        *messageWidget

	templates []*apprunnerv1.CloudInitTemplate
	selected  string
	creating  bool
	savedName string
	savedData string
	loading   bool
	busy      bool

	saveAction, duplicateAction, deleteAction, machineAction *qt6.QAction
}

func newCloudInitView(mw *mainWindow) *cloudInitView {
	v := &cloudInitView{mw: mw}
	v.root = qt6.NewQWidget2()
	layout := qt6.NewQVBoxLayout(v.root)
	layout.SetContentsMargins(0, 0, 0, 0)
	layout.SetSpacing(0)
	v.errors = newMessageWidget(true)
	layout.AddWidget(v.errors.QWidget)
	splitter := qt6.NewQSplitter3(qt6.Horizontal)
	splitter.SetChildrenCollapsible(false)

	listPane := qt6.NewQWidget2()
	listLayout := qt6.NewQVBoxLayout(listPane)
	listLayout.SetContentsMargins(0, 0, 0, 0)
	listLayout.SetSpacing(0)
	v.filter = qt6.NewQLineEdit2()
	v.filter.SetPlaceholderText("Filter templates…")
	v.filter.SetClearButtonEnabled(true)
	v.filter.OnTextChanged(func(string) { v.applyFilter() })
	filterRow := qt6.NewQHBoxLayout2()
	filterRow.SetContentsMargins(6, 6, 6, 6)
	filterRow.AddWidget(v.filter.QWidget)
	listLayout.AddLayout(filterRow.QLayout)
	v.list = qt6.NewQListWidget2()
	v.list.SetFrameShape(qt6.QFrame__NoFrame)
	v.list.OnCurrentItemChanged(func(current, previous *qt6.QListWidgetItem) {
		if v.loading || current == nil {
			return
		}
		name := current.Data(int(qt6.UserRole)).ToString()
		if name == v.selected && !v.creating {
			return
		}
		if !v.confirmDiscard() {
			v.loading = true
			v.selectInList(v.selected)
			v.loading = false
			return
		}
		v.load(v.template(name))
	})
	listLayout.AddWidget2(v.list.QWidget, 1)
	note := mutedLabel("Templates are stored privately in state/cloud-init-templates.json. Loading one into a VM copies its contents.")
	note.SetContentsMargins(8, 6, 8, 6)
	listLayout.AddWidget(horizontalLine().QWidget)
	listLayout.AddWidget(note.QWidget)
	listPane.SetMinimumWidth(200)
	splitter.AddWidget(listPane)

	editorPane := qt6.NewQWidget2()
	editorLayout := qt6.NewQVBoxLayout(editorPane)
	editorLayout.SetContentsMargins(0, 0, 0, 0)
	editorLayout.SetSpacing(0)
	nameRow := qt6.NewQHBoxLayout2()
	nameRow.SetContentsMargins(10, 8, 10, 8)
	nameRow.AddWidget(qt6.NewQLabel3("Name:").QWidget)
	v.name = qt6.NewQLineEdit2()
	v.name.SetMaxLength(maxTemplateName)
	v.name.SetMaximumWidth(360)
	v.name.OnTextChanged(func(string) { v.changed() })
	nameRow.AddWidget(v.name.QWidget)
	nameRow.AddWidget(inlineLabel(mutedHTML("Maximum 80 characters")).QWidget)
	nameRow.AddStretch()
	editorLayout.AddLayout(nameRow.QLayout)
	v.editor = newCodeEditor()
	v.editor.SetFrameShape(qt6.QFrame__NoFrame)
	v.editor.OnTextChanged(func() { v.changed() })
	v.editor.OnCursorPositionChanged(func() { v.updatePosition() })
	editorLayout.AddWidget2(v.editor.QWidget, 1)
	footer := qt6.NewQHBoxLayout2()
	footer.SetContentsMargins(10, 4, 10, 4)
	v.verdictLabel = inlineLabel("")
	footer.AddWidget(v.verdictLabel.QWidget)
	footer.AddWidget(verticalLine().QWidget)
	v.usedLabel = inlineLabel("")
	footer.AddWidget(v.usedLabel.QWidget)
	footer.AddStretch()
	v.positionLabel = inlineLabel("")
	footer.AddWidget(v.positionLabel.QWidget)
	editorLayout.AddWidget(horizontalLine().QWidget)
	editorLayout.AddLayout(footer.QLayout)
	splitter.AddWidget(editorPane)
	splitter.SetStretchFactor(1, 1)
	splitter.SetSizes([]int{260, 800})
	layout.AddWidget2(splitter.QWidget, 1)

	v.tb = qt6.NewQToolBar2("Cloud-init")
	v.tb.AddAction(mw.action("&New Template", "document-new", qt6.QStyle__SP_FileIcon, "", func() { v.newTemplate() }))
	v.saveAction = mw.action("&Save", "document-save", qt6.QStyle__SP_DialogSaveButton, "Ctrl+S", func() { v.save() })
	v.tb.AddAction(v.saveAction)
	v.duplicateAction = mw.action("D&uplicate", "edit-copy", 0, "", func() { v.duplicate() })
	v.tb.AddAction(v.duplicateAction)
	v.deleteAction = mw.action("&Delete", "edit-delete", qt6.QStyle__SP_TrashIcon, "", func() { v.remove() })
	v.tb.AddAction(v.deleteAction)
	v.tb.AddSeparator()
	v.tb.AddAction(mw.action("&Validate YAML", "dialog-ok-apply", qt6.QStyle__SP_DialogApplyButton, "", func() { v.validate(true) }))
	v.machineAction = mw.action("New &Machine with Template…", "computer", qt6.QStyle__SP_ComputerIcon, "", func() {
		mw.openWizardWithTemplate(&apprunnerv1.CloudInitTemplate{Name: strings.TrimSpace(v.name.Text()), UserData: v.editor.ToPlainText()})
	})
	v.tb.AddAction(v.machineAction)
	v.updateActions()
	return v
}

func (v *cloudInitView) widget() *qt6.QWidget   { return v.root }
func (v *cloudInitView) toolbar() *qt6.QToolBar { return v.tb }

func (v *cloudInitView) title() string {
	name := strings.TrimSpace(v.name.Text())
	if name == "" {
		name = "New template"
	}
	if v.dirty() {
		name += " [modified]"
	}
	return name + " — Cloud-init Templates"
}

func (v *cloudInitView) template(name string) *apprunnerv1.CloudInitTemplate {
	for _, template := range v.templates {
		if template.GetName() == name {
			return template
		}
	}
	return nil
}

func (v *cloudInitView) dirty() bool {
	if v.creating {
		return strings.TrimSpace(v.name.Text()) != "" || strings.TrimSpace(v.editor.ToPlainText()) != strings.TrimSpace(v.savedData)
	}
	return v.selected != "" && (strings.TrimSpace(v.name.Text()) != v.savedName || v.editor.ToPlainText() != v.savedData)
}

func (v *cloudInitView) valid() bool {
	name := strings.TrimSpace(v.name.Text())
	if name == "" || len(name) > maxTemplateName || len(v.editor.ToPlainText()) > maxUserDataSize {
		return false
	}
	for _, template := range v.templates {
		if strings.EqualFold(template.GetName(), name) && template.GetName() != v.selected {
			return false
		}
	}
	return true
}

func (v *cloudInitView) refresh(s *snapshot) {
	templates := append([]*apprunnerv1.CloudInitTemplate(nil), s.templates...)
	sort.Slice(templates, func(i, j int) bool {
		return strings.ToLower(templates[i].GetName()) < strings.ToLower(templates[j].GetName())
	})
	v.templates = templates
	v.loading = true
	v.list.Clear()
	for _, template := range templates {
		item := qt6.NewQListWidgetItem2(template.GetName() + "\n" + "Updated " + formatTime(template.GetUpdatedAt()) + " · " + pluralize(strings.Count(template.GetUserData(), "\n")+1, "line", "lines"))
		item.SetData(int(qt6.UserRole), qt6.NewQVariant14(template.GetName()))
		v.list.AddItemWithItem(item)
	}
	v.applyFilter()
	v.loading = false
	switch {
	case v.creating:
		v.updateUsedBy()
	case v.selected != "" && v.template(v.selected) != nil:
		v.loading = true
		v.selectInList(v.selected)
		v.loading = false
		if template := v.template(v.selected); !v.dirty() && (template.GetUserData() != v.savedData || template.GetName() != v.savedName) {
			v.load(template)
		} else {
			v.updateUsedBy()
		}
	case v.selected != "" && v.dirty():
		// The template vanished on the server; keep the edits as a draft.
		v.creating = true
		v.selected = ""
		v.savedName = ""
		v.savedData = ""
		v.changed()
	case len(templates) > 0:
		v.load(templates[0])
	default:
		v.newTemplate()
	}
	v.updateActions()
}

func (v *cloudInitView) applyFilter() {
	needle := strings.ToLower(strings.TrimSpace(v.filter.Text()))
	for i := 0; i < v.list.Count(); i++ {
		item := v.list.Item(i)
		item.SetHidden(needle != "" && !strings.Contains(strings.ToLower(item.Data(int(qt6.UserRole)).ToString()), needle))
	}
}

func (v *cloudInitView) selectInList(name string) {
	for i := 0; i < v.list.Count(); i++ {
		if item := v.list.Item(i); item.Data(int(qt6.UserRole)).ToString() == name {
			v.list.SetCurrentItem(item)
			return
		}
	}
}

func (v *cloudInitView) load(template *apprunnerv1.CloudInitTemplate) {
	if template == nil {
		return
	}
	v.loading = true
	v.creating = false
	v.selected = template.GetName()
	v.savedName = template.GetName()
	v.savedData = template.GetUserData()
	v.name.SetText(template.GetName())
	v.editor.SetPlainText(template.GetUserData())
	v.selectInList(template.GetName())
	v.loading = false
	v.errors.Hide()
	v.changed()
}

func (v *cloudInitView) newTemplate() {
	if !v.confirmDiscard() {
		return
	}
	v.loading = true
	v.creating = true
	v.selected = ""
	v.savedName = ""
	v.savedData = "#cloud-config\n"
	v.name.SetText("")
	v.editor.SetPlainText(v.savedData)
	v.list.SetCurrentRow(-1)
	v.loading = false
	v.errors.Hide()
	v.changed()
	v.name.SetFocus()
}

func (v *cloudInitView) duplicate() {
	content := v.editor.ToPlainText()
	base := strings.TrimSpace(v.name.Text())
	name := base + "-copy"
	for index := 2; v.template(name) != nil && index < 100; index++ {
		name = fmt.Sprintf("%s-copy-%d", base, index)
	}
	if !v.confirmDiscard() {
		return
	}
	v.loading = true
	v.creating = true
	v.selected = ""
	v.savedName = ""
	v.savedData = ""
	v.name.SetText(name)
	v.editor.SetPlainText(content)
	v.list.SetCurrentRow(-1)
	v.loading = false
	v.changed()
}

// confirmDiscard asks before leaving unsaved changes; it returns false when
// the user wants to stay.
func (v *cloudInitView) confirmDiscard() bool {
	if !v.dirty() {
		return true
	}
	box := qt6.NewQMessageBox(v.mw.QWidget)
	box.SetWindowTitle("Unsaved Changes")
	box.SetIcon(qt6.QMessageBox__Warning)
	box.SetText(fmt.Sprintf("The template %q has unsaved changes.", firstNonEmpty(strings.TrimSpace(v.name.Text()), "New template")))
	save := box.AddButton2("Save", qt6.QMessageBox__AcceptRole)
	discard := box.AddButton2("Discard", qt6.QMessageBox__DestructiveRole)
	cancel := box.AddButton2("Cancel", qt6.QMessageBox__RejectRole)
	box.SetDefaultButton(save)
	box.SetEscapeButton(cancel.QAbstractButton)
	save.SetEnabled(v.valid())
	box.Exec()
	clicked := box.ClickedButton()
	switch {
	case clicked != nil && clicked.UnsafePointer() == discard.QAbstractButton.UnsafePointer():
		return true
	case clicked != nil && clicked.UnsafePointer() == save.QAbstractButton.UnsafePointer():
		v.save()
		return false
	default:
		return false
	}
}

func (v *cloudInitView) changed() {
	if v.loading {
		return
	}
	v.validate(false)
	v.updateUsedBy()
	v.updatePosition()
	v.updateActions()
	v.mw.updateTitle()
}

func (v *cloudInitView) validate(announce bool) yamlVerdict {
	verdict := validateUserData(v.editor.ToPlainText())
	v.verdictLabel.SetText(dotHTML(verdict.color) + " " + escape(verdict.message))
	if announce {
		if verdict.ok {
			qt6.QMessageBox_Information(v.mw.QWidget, "Validate YAML", verdict.message)
		} else {
			qt6.QMessageBox_Warning(v.mw.QWidget, "Validate YAML", verdict.message)
		}
	}
	return verdict
}

// updateUsedBy lists machines whose cloud-init user-data matches this
// template; loading a template copies its contents, so equality is the only
// link.
func (v *cloudInitView) updateUsedBy() {
	content := v.editor.ToPlainText()
	var names []string
	if strings.TrimSpace(content) != "" {
		for _, vm := range v.mw.snapshot.vms {
			if vm.GetCloudInit().GetEnabled() && vm.GetCloudInit().GetUserData() == content {
				names = append(names, vm.GetName())
			}
		}
	}
	if len(names) == 0 {
		v.usedLabel.SetText(mutedHTML("Not used by any machine"))
	} else {
		v.usedLabel.SetText(mutedHTML("Used by: ") + escape(strings.Join(names, ", ")))
	}
}

func (v *cloudInitView) updatePosition() {
	cursor := v.editor.TextCursor()
	size := len(v.editor.ToPlainText())
	v.positionLabel.SetText(mutedHTML(fmt.Sprintf("Line %d, Col %d · YAML · UTF-8 · %s of 1 MiB", cursor.BlockNumber()+1, cursor.PositionInBlock()+1, formatBytes(uint64(size)))))
}

func (v *cloudInitView) updateActions() {
	v.saveAction.SetEnabled(!v.busy && v.dirty() && v.valid())
	v.duplicateAction.SetEnabled(!v.busy && strings.TrimSpace(v.name.Text()) != "")
	v.deleteAction.SetEnabled(!v.busy && !v.creating && v.selected != "")
	v.machineAction.SetEnabled(!v.busy)
}

func (v *cloudInitView) statusText() string {
	return fmt.Sprintf("%s   |   Written verbatim to the VM's NoCloud ISO (xorriso)", pluralize(len(v.templates), "template", "templates"))
}

func (v *cloudInitView) setBusy(busy bool) {
	v.busy = busy
	v.updateActions()
}

func (v *cloudInitView) save() {
	if !v.valid() || v.busy {
		return
	}
	name := strings.TrimSpace(v.name.Text())
	data := v.editor.ToPlainText()
	creating := v.creating
	previous := v.selected
	api := v.mw.client.api
	v.errors.Hide()
	v.setBusy(true)
	call(func(ctx context.Context) (*apprunnerv1.CloudInitTemplate, error) {
		if creating {
			r, err := api.CreateCloudInitTemplate(ctx, &apprunnerv1.CreateCloudInitTemplateRequest{Name: name, UserData: data})
			return r.GetTemplate(), err
		}
		r, err := api.UpdateCloudInitTemplate(ctx, &apprunnerv1.UpdateCloudInitTemplateRequest{Name: previous, NewName: name, UserData: data})
		return r.GetTemplate(), err
	}, func(saved *apprunnerv1.CloudInitTemplate, err error) {
		v.setBusy(false)
		if err != nil {
			v.errors.show(messageError, escape(errorMessage(err)))
			return
		}
		v.creating = false
		v.selected = saved.GetName()
		v.savedName = saved.GetName()
		v.savedData = saved.GetUserData()
		v.changed()
		v.mw.StatusBar().ShowMessage2("Saved "+saved.GetName(), 4000)
		v.mw.refresh()
	})
}

func (v *cloudInitView) remove() {
	if v.creating || v.selected == "" {
		return
	}
	name := v.selected
	if !confirm(v.mw.QWidget, "Delete Template", fmt.Sprintf("Delete the cloud-init template %q? Machines that loaded it keep their copies.", name), "Delete", true) {
		return
	}
	api := v.mw.client.api
	v.errors.Hide()
	v.setBusy(true)
	call(func(ctx context.Context) (struct{}, error) {
		_, err := api.DeleteCloudInitTemplate(ctx, &apprunnerv1.DeleteCloudInitTemplateRequest{Name: name})
		return struct{}{}, err
	}, func(_ struct{}, err error) {
		v.setBusy(false)
		if err != nil {
			v.errors.show(messageError, escape(errorMessage(err)))
			return
		}
		v.selected = ""
		v.savedName = ""
		v.savedData = ""
		v.creating = true
		v.loading = true
		v.name.SetText("")
		v.editor.SetPlainText("")
		v.loading = false
		v.mw.StatusBar().ShowMessage2("Deleted "+name, 4000)
		v.mw.refresh()
	})
}
