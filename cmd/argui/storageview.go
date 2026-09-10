package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mappu/miqt/qt6"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
)

// storageView is the Dolphin-style details view of one storage root.
type storageView struct {
	mw       *mainWindow
	root     *qt6.QWidget
	tb       *qt6.QToolBar
	rootName string
	crumb    *qt6.QLabel
	summary  *qt6.QLabel
	table    *qt6.QTreeWidget
	details  *qt6.QLabel
	empty    *qt6.QLabel

	entries    map[string]*apprunnerv1.StorageEntry
	uses       map[string][]storageUse
	rootBytes  map[string]uint64
	rootFiles  map[string]int
	newMachine *qt6.QAction
}

func newStorageView(mw *mainWindow) *storageView {
	v := &storageView{mw: mw, rootName: "iso", entries: map[string]*apprunnerv1.StorageEntry{}, rootBytes: map[string]uint64{}, rootFiles: map[string]int{}}
	v.root = qt6.NewQWidget2()
	layout := qt6.NewQVBoxLayout(v.root)
	layout.SetContentsMargins(0, 0, 0, 0)
	layout.SetSpacing(0)

	crumbRow := qt6.NewQHBoxLayout2()
	crumbRow.SetContentsMargins(10, 6, 10, 6)
	v.crumb = richLabel("")
	crumbRow.AddWidget(v.crumb.QWidget)
	crumbRow.AddStretch()
	v.summary = mutedLabel("")
	crumbRow.AddWidget(v.summary.QWidget)
	layout.AddLayout(crumbRow.QLayout)

	v.table = newTable("Name", "Size", "Modified", "Used by")
	v.table.SetRootIsDecorated(true)
	v.table.SetFrameShape(qt6.QFrame__NoFrame)
	v.table.SetSortingEnabled(true)
	v.table.SortItems(0, qt6.AscendingOrder)
	v.table.Header().SetSectionResizeMode2(0, qt6.QHeaderView__Stretch)
	v.table.Header().SetSectionResizeMode2(1, qt6.QHeaderView__ResizeToContents)
	v.table.Header().SetSectionResizeMode2(2, qt6.QHeaderView__ResizeToContents)
	v.table.Header().SetSectionResizeMode2(3, qt6.QHeaderView__Interactive)
	v.table.Header().SetStretchLastSection(false)
	v.table.SetColumnWidth(3, 260)
	v.table.OnItemSelectionChanged(func() { v.updateDetails() })
	v.table.OnItemDoubleClicked(func(item *qt6.QTreeWidgetItem, _ int) {
		if entry := v.entries[itemData(item)]; entry != nil && v.canCreateMachine(entry) {
			mw.openWizard(v.rootName, entry.GetPath())
		}
	})
	layout.AddWidget2(v.table.QWidget, 1)
	v.empty = richLabel("")
	v.empty.SetAlignment(qt6.AlignCenter)
	v.empty.Hide()
	layout.AddWidget2(v.empty.QWidget, 1)

	v.details = richLabel("")
	v.details.SetContentsMargins(10, 6, 10, 6)
	v.details.SetTextInteractionFlags(qt6.TextSelectableByMouse)
	layout.AddWidget(horizontalLine().QWidget)
	layout.AddWidget(v.details.QWidget)

	v.tb = qt6.NewQToolBar2("Storage")
	v.tb.AddAction(mw.action("&Upload…", "document-import", 0, "", func() { v.openUpload(v.rootName, v.selectedDirectory()) }))
	v.tb.AddAction(mw.action("&Download from URL…", "download", 0, "", func() { v.openDownload(v.rootName, v.selectedDirectory()) }))
	v.tb.AddSeparator()
	v.newMachine = mw.action("New &Machine from Image…", "list-add", qt6.QStyle__SP_FileDialogNewFolder, "", func() { v.newMachineFromSelection() })
	v.tb.AddAction(v.newMachine)
	v.tb.AddWidget(spacer())
	v.tb.AddAction(mw.refreshAction)
	return v
}

func (v *storageView) widget() *qt6.QWidget   { return v.root }
func (v *storageView) toolbar() *qt6.QToolBar { return v.tb }
func (v *storageView) title() string          { return v.rootName + "/ — Storage" }

func (v *storageView) setRoot(name string) {
	if v.rootName != name {
		v.rootName = name
		v.refresh(v.mw.snapshot)
	}
}

func (v *storageView) selectedEntry() *apprunnerv1.StorageEntry {
	return v.entries[itemData(v.table.CurrentItem())]
}

// selectedDirectory is the directory an upload should target: the selected
// directory, or the selected file's parent.
func (v *storageView) selectedDirectory() string {
	entry := v.selectedEntry()
	if entry == nil {
		return ""
	}
	if entry.GetKind() == apprunnerv1.StorageEntryKind_STORAGE_ENTRY_KIND_DIRECTORY {
		return entry.GetPath()
	}
	if index := strings.LastIndex(entry.GetPath(), "/"); index > 0 {
		return entry.GetPath()[:index]
	}
	return ""
}

func (v *storageView) canCreateMachine(entry *apprunnerv1.StorageEntry) bool {
	return v.rootName == "iso" && entry.GetKind() != apprunnerv1.StorageEntryKind_STORAGE_ENTRY_KIND_DIRECTORY && (cloudImageName.MatchString(entry.GetName()) || isoName.MatchString(entry.GetName()))
}

func (v *storageView) newMachineFromSelection() {
	entry := v.selectedEntry()
	if entry != nil && v.canCreateMachine(entry) {
		v.mw.openWizard(v.rootName, entry.GetPath())
		return
	}
	v.mw.openWizard("", "")
}

func (v *storageView) fileKind(entry *apprunnerv1.StorageEntry) (label, icon string, fallback qt6.QStyle__StandardPixmap) {
	switch {
	case entry.GetKind() == apprunnerv1.StorageEntryKind_STORAGE_ENTRY_KIND_DIRECTORY:
		return "Folder", "folder", qt6.QStyle__SP_DirIcon
	case v.rootName == "iso" && cloudImageName.MatchString(entry.GetName()):
		return "qcow2 cloud image", "drive-harddisk", qt6.QStyle__SP_DriveHDIcon
	case v.rootName == "iso" && isoName.MatchString(entry.GetName()):
		return "ISO image", "media-optical", qt6.QStyle__SP_DriveCDIcon
	case v.rootName == "disk":
		return "Virtual machine disk", "drive-harddisk", qt6.QStyle__SP_DriveHDIcon
	default:
		return "File", "text-x-generic", qt6.QStyle__SP_FileIcon
	}
}

func (v *storageView) refresh(s *snapshot) {
	selected := itemData(v.table.CurrentItem())
	v.uses = storageUses(s.vms)
	v.entries = map[string]*apprunnerv1.StorageEntry{}
	v.rootBytes = map[string]uint64{}
	v.rootFiles = map[string]int{}
	for _, root := range s.storage {
		bytes, files := sumEntries(root.GetEntries())
		v.rootBytes[root.GetName()] = bytes
		v.rootFiles[root.GetName()] = files
	}
	v.table.SetSortingEnabled(false)
	v.table.Clear()
	root := s.storageRoot(v.rootName)
	var add func(parent *qt6.QTreeWidgetItem, entries []*apprunnerv1.StorageEntry)
	add = func(parent *qt6.QTreeWidgetItem, entries []*apprunnerv1.StorageEntry) {
		for _, entry := range entries {
			v.entries[entry.GetPath()] = entry
			directory := entry.GetKind() == apprunnerv1.StorageEntryKind_STORAGE_ENTRY_KIND_DIRECTORY
			size, modified := "", ""
			if !directory {
				size = formatBytes(entry.GetSizeBytes())
				modified = formatTime(entry.GetModifiedAt())
			}
			var users []string
			for _, use := range v.uses[v.rootName+":"+entry.GetPath()] {
				users = append(users, use.vm.GetName())
			}
			item := qt6.NewQTreeWidgetItem2([]string{entry.GetName(), size, modified, strings.Join(users, ", ")})
			_, icon, fallback := v.fileKind(entry)
			item.SetIcon(0, themeIcon(icon, fallback))
			item.SetTextAlignment(1, int(qt6.AlignRight|qt6.AlignVCenter))
			setItemData(item, entry.GetPath())
			if len(users) > 0 {
				var tips []string
				for _, use := range v.uses[v.rootName+":"+entry.GetPath()] {
					tips = append(tips, use.vm.GetName()+" · "+use.label)
				}
				item.SetToolTip(3, strings.Join(tips, "\n"))
			}
			if parent == nil {
				v.table.AddTopLevelItem(item)
			} else {
				parent.AddChild(item)
			}
			if directory {
				add(item, entry.GetChildren())
				item.SetExpanded(true)
			}
		}
	}
	add(nil, root.GetEntries())
	v.table.SetSortingEnabled(true)
	if selected != "" {
		v.selectPath(v.table.InvisibleRootItem(), selected)
	}
	v.crumb.SetText(mutedHTML("app-runner") + " " + mutedHTML("›") + " <b>" + escape(v.rootName) + "</b>")
	v.summary.SetText(mutedHTML(fmt.Sprintf("%s · %s", pluralize(v.rootFiles[v.rootName], "file", "files"), formatBytes(v.rootBytes[v.rootName]))))
	if root != nil && len(root.GetEntries()) == 0 {
		v.empty.SetText("<h3>" + escape(v.rootName+"/") + " is empty</h3>" + mutedHTML("Upload files or download them from a URL with the toolbar."))
		v.empty.Show()
		v.table.Hide()
	} else {
		v.empty.Hide()
		v.table.Show()
	}
	v.updateDetails()
}

func (v *storageView) selectPath(parent *qt6.QTreeWidgetItem, path string) bool {
	for i := 0; i < parent.ChildCount(); i++ {
		child := parent.Child(i)
		if itemData(child) == path {
			v.table.SetCurrentItem(child)
			return true
		}
		if v.selectPath(child, path) {
			return true
		}
	}
	return false
}

func sumEntries(entries []*apprunnerv1.StorageEntry) (bytes uint64, files int) {
	for _, entry := range entries {
		if entry.GetKind() == apprunnerv1.StorageEntryKind_STORAGE_ENTRY_KIND_DIRECTORY {
			childBytes, childFiles := sumEntries(entry.GetChildren())
			bytes += childBytes
			files += childFiles
		} else {
			bytes += entry.GetSizeBytes()
			files++
		}
	}
	return bytes, files
}

func (v *storageView) updateDetails() {
	entry := v.selectedEntry()
	v.newMachine.SetEnabled(entry != nil && v.canCreateMachine(entry))
	if entry == nil {
		v.details.SetText(mutedHTML("Select a file to see which machines use it."))
		v.mw.updateTitle()
		return
	}
	kind, _, _ := v.fileKind(entry)
	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>%s</b> · %s", mutedHTML("Selected:"), escape(entry.GetPath()), escape(kind))
	if entry.GetKind() != apprunnerv1.StorageEntryKind_STORAGE_ENTRY_KIND_DIRECTORY {
		fmt.Fprintf(&b, " · %s", formatBytes(entry.GetSizeBytes()))
	}
	uses := v.uses[v.rootName+":"+entry.GetPath()]
	if len(uses) > 0 {
		var names []string
		backing := false
		for _, use := range uses {
			names = append(names, fmt.Sprintf("%s (%s)", escape(use.vm.GetName()), escape(strings.ToLower(use.label))))
			backing = backing || use.label == "Cloud image"
		}
		sort.Strings(names)
		if backing {
			fmt.Fprintf(&b, "<br>%s %s — linked overlays; keep this file in place.", mutedHTML("Backing for:"), strings.Join(names, ", "))
		} else {
			fmt.Fprintf(&b, "<br>%s %s", mutedHTML("Used by:"), strings.Join(names, ", "))
		}
	} else if entry.GetKind() != apprunnerv1.StorageEntryKind_STORAGE_ENTRY_KIND_DIRECTORY {
		fmt.Fprintf(&b, "<br>%s", mutedHTML("Not used by any virtual machine."))
	}
	v.details.SetText(b.String())
	v.mw.updateTitle()
}

func (v *storageView) statusText() string {
	parts := []string{}
	if entry := v.selectedEntry(); entry != nil && entry.GetKind() != apprunnerv1.StorageEntryKind_STORAGE_ENTRY_KIND_DIRECTORY {
		parts = append(parts, fmt.Sprintf("1 of %d selected (%s)", v.rootFiles[v.rootName], formatBytes(entry.GetSizeBytes())))
	} else {
		parts = append(parts, pluralize(v.rootFiles[v.rootName], "file", "files"))
	}
	parts = append(parts, fmt.Sprintf("iso/ %s · disk/ %s", formatBytes(v.rootBytes["iso"]), formatBytes(v.rootBytes["disk"])))
	return strings.Join(parts, "   |   ")
}

func (v *storageView) openUpload(root, directory string) *qt6.QWidget {
	return openStorageDialog(v.mw, firstNonEmpty(root, v.rootName), directory, false)
}

func (v *storageView) openDownload(root, directory string) *qt6.QWidget {
	return openStorageDialog(v.mw, firstNonEmpty(root, v.rootName), directory, true)
}

// directoriesUnder lists a root's directories for the destination combo.
func directoriesUnder(root *apprunnerv1.StorageRoot) []string {
	var paths []string
	var walk func(entries []*apprunnerv1.StorageEntry)
	walk = func(entries []*apprunnerv1.StorageEntry) {
		for _, entry := range entries {
			if entry.GetKind() == apprunnerv1.StorageEntryKind_STORAGE_ENTRY_KIND_DIRECTORY {
				paths = append(paths, entry.GetPath())
				walk(entry.GetChildren())
			}
		}
	}
	walk(root.GetEntries())
	sort.Strings(paths)
	return paths
}
