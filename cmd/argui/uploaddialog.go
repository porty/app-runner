package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mappu/miqt/qt6"
	"github.com/mappu/miqt/qt6/mainthread"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
)

var httpURL = regexp.MustCompile(`(?i)^https?://`)

// storageDialog is "Add to Storage": upload files from this device or have
// the server download a URL into iso/ or disk/.
type storageDialog struct {
	*qt6.QDialog
	mw          *mainWindow
	tabs        *qt6.QTabWidget
	isoRoot     *qt6.QRadioButton
	diskRoot    *qt6.QRadioButton
	destination *qt6.QComboBox
	files       *qt6.QListWidget
	fileNote    *qt6.QLabel
	progress    *qt6.QProgressBar
	url         *qt6.QLineEdit
	filename    *qt6.QLineEdit
	errors      *messageWidget
	submit      *qt6.QPushButton
	cancel      *qt6.QPushButton
	paths       []string
	busy        bool
	cancelFn    context.CancelFunc
}

func openStorageDialog(mw *mainWindow, root, directory string, fromURL bool) *qt6.QWidget {
	d := &storageDialog{QDialog: qt6.NewQDialog(mw.QWidget), mw: mw}
	d.SetWindowTitle("Add to Storage")
	d.SetAttribute(qt6.WA_DeleteOnClose)
	d.SetMinimumWidth(540)
	d.SetAcceptDrops(true)
	layout := qt6.NewQVBoxLayout(d.QWidget)
	layout.SetSpacing(10)

	d.tabs = qt6.NewQTabWidget2()
	d.tabs.SetDocumentMode(true)
	d.tabs.AddTab(d.buildDeviceTab(), "From this device")
	d.tabs.AddTab(d.buildURLTab(), "From a URL")
	layout.AddWidget(d.tabs.QWidget)

	form := qt6.NewQFormLayout2()
	fillForm(form)
	roots := qt6.NewQHBoxLayout2()
	d.isoRoot = qt6.NewQRadioButton3("iso/")
	d.diskRoot = qt6.NewQRadioButton3("disk/")
	roots.AddWidget(d.isoRoot.QWidget)
	roots.AddWidget(d.diskRoot.QWidget)
	roots.AddStretch()
	form.AddRow4("Storage root:", roots.QLayout)
	d.destination = qt6.NewQComboBox2()
	form.AddRow3("Destination:", d.destination.QWidget)
	layout.InsertLayout(0, form.QLayout)
	d.errors = newMessageWidget(true)
	layout.AddWidget(d.errors.QWidget)
	d.progress = qt6.NewQProgressBar2()
	d.progress.SetRange(0, 100)
	d.progress.SetTextVisible(true)
	d.progress.Hide()
	layout.AddWidget(d.progress.QWidget)

	buttons := qt6.NewQHBoxLayout2()
	buttons.AddStretch()
	d.submit = qt6.NewQPushButton3("Upload")
	d.submit.SetDefault(true)
	d.submit.OnClicked(func() { d.start() })
	d.cancel = qt6.NewQPushButton3("Cancel")
	d.cancel.OnClicked(func() {
		if d.cancelFn != nil {
			d.cancelFn()
		}
		d.Reject()
	})
	buttons.AddWidget(d.submit.QWidget)
	buttons.AddWidget(d.cancel.QWidget)
	layout.AddLayout(buttons.QLayout)

	d.isoRoot.OnToggled(func(bool) { d.rebuildDestinations("") })
	d.diskRoot.OnToggled(func(bool) { d.rebuildDestinations("") })
	if root == "disk" {
		d.diskRoot.SetChecked(true)
	} else {
		d.isoRoot.SetChecked(true)
	}
	d.rebuildDestinations(directory)
	d.tabs.OnCurrentChanged(func(int) { d.update() })
	if fromURL {
		d.tabs.SetCurrentIndex(1)
	}
	d.OnDragEnterEvent(func(super func(event *qt6.QDragEnterEvent), event *qt6.QDragEnterEvent) {
		if event.MimeData().HasUrls() && !d.busy {
			event.AcceptProposedAction()
		}
	})
	d.OnDropEvent(func(super func(event *qt6.QDropEvent), event *qt6.QDropEvent) {
		urls := event.MimeData().Urls()
		for index := range urls {
			if urls[index].IsLocalFile() {
				d.addPath(urls[index].ToLocalFile())
			}
		}
		d.tabs.SetCurrentIndex(0)
		event.AcceptProposedAction()
		d.update()
	})
	d.update()
	d.Show()
	return d.QWidget
}

func (d *storageDialog) buildDeviceTab() *qt6.QWidget {
	tab := qt6.NewQWidget2()
	layout := qt6.NewQVBoxLayout(tab)
	d.files = qt6.NewQListWidget2()
	d.files.SetMinimumHeight(120)
	d.files.SetSelectionMode(qt6.QAbstractItemView__ExtendedSelection)
	layout.AddWidget(d.files.QWidget)
	row := qt6.NewQHBoxLayout2()
	browse := qt6.NewQPushButton4(themeIcon("document-open", 0), "Browse…")
	browse.OnClicked(func() {
		for _, path := range qt6.QFileDialog_GetOpenFileNames4(d.QWidget, "Choose files to upload", "", "Images (*.iso *.img *.qcow2);;All files (*)") {
			d.addPath(path)
		}
		d.update()
	})
	remove := qt6.NewQPushButton3("Remove")
	remove.OnClicked(func() {
		var keep []string
		for index, path := range d.paths {
			if item := d.files.Item(index); item == nil || !item.IsSelected() {
				keep = append(keep, path)
			}
		}
		d.paths = keep
		d.rebuildFiles()
		d.update()
	})
	row.AddWidget(browse.QWidget)
	row.AddWidget(remove.QWidget)
	row.AddStretch()
	d.fileNote = hint("Drop files here or choose them with Browse….")
	row.AddWidget(d.fileNote.QWidget)
	layout.AddLayout(row.QLayout)
	return tab
}

func (d *storageDialog) buildURLTab() *qt6.QWidget {
	tab := qt6.NewQWidget2()
	form := qt6.NewQFormLayout(tab)
	fillForm(form)
	d.url = qt6.NewQLineEdit2()
	d.url.SetPlaceholderText("https://example.com/image.iso")
	d.url.OnTextChanged(func(string) { d.update() })
	form.AddRow3("File URL:", d.url.QWidget)
	form.AddRow3("", hint("The App Runner host downloads the file directly.").QWidget)
	d.filename = qt6.NewQLineEdit2()
	d.filename.SetPlaceholderText("Inferred from the URL")
	form.AddRow3("Filename:", d.filename.QWidget)
	return tab
}

func (d *storageDialog) rootName() string {
	if d.diskRoot.IsChecked() {
		return "disk"
	}
	return "iso"
}

func (d *storageDialog) rebuildDestinations(selected string) {
	d.destination.Clear()
	root := d.rootName()
	d.destination.AddItem3(root+"/ (root)", qt6.NewQVariant14(""))
	for _, path := range directoriesUnder(d.mw.snapshot.storageRoot(root)) {
		d.destination.AddItem3(root+"/"+path+"/", qt6.NewQVariant14(path))
	}
	if index := d.destination.FindData(qt6.NewQVariant14(selected)); index >= 0 {
		d.destination.SetCurrentIndex(index)
	}
}

func (d *storageDialog) addPath(path string) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return
	}
	for _, existing := range d.paths {
		if existing == path {
			return
		}
	}
	d.paths = append(d.paths, path)
	d.rebuildFiles()
}

func (d *storageDialog) rebuildFiles() {
	d.files.Clear()
	for _, path := range d.paths {
		size := uint64(0)
		if info, err := os.Stat(path); err == nil {
			size = uint64(info.Size())
		}
		item := qt6.NewQListWidgetItem2(fmt.Sprintf("%s  ·  %s", filepath.Base(path), formatBytes(size)))
		item.SetToolTip(path)
		d.files.AddItemWithItem(item)
	}
}

func (d *storageDialog) fromURL() bool {
	return d.tabs.CurrentIndex() == 1
}

func (d *storageDialog) update() {
	valid := false
	if d.fromURL() {
		d.submit.SetText("Download")
		valid = httpURL.MatchString(strings.TrimSpace(d.url.Text()))
	} else {
		d.submit.SetText("Upload")
		valid = len(d.paths) > 0
	}
	if d.busy {
		if d.fromURL() {
			d.submit.SetText("Downloading…")
		} else {
			d.submit.SetText("Uploading…")
		}
	}
	d.submit.SetEnabled(valid && !d.busy)
	d.tabs.SetEnabled(!d.busy)
	d.isoRoot.SetEnabled(!d.busy)
	d.diskRoot.SetEnabled(!d.busy)
	d.destination.SetEnabled(!d.busy)
}

func (d *storageDialog) start() {
	if d.busy {
		return
	}
	root := d.rootName()
	directory := d.destination.CurrentData().ToString()
	ctx, cancel := context.WithCancel(context.Background())
	d.cancelFn = cancel
	d.busy = true
	d.errors.Hide()
	d.update()
	if d.fromURL() {
		url := strings.TrimSpace(d.url.Text())
		filename := strings.TrimSpace(d.filename.Text())
		d.progress.SetRange(0, 0)
		d.progress.Show()
		call(func(context.Context) (*apprunnerv1.StorageEntry, error) {
			r, err := d.mw.client.slow.DownloadStorageFile(ctx, &apprunnerv1.DownloadStorageFileRequest{Root: root, Directory: directory, Url: url, Filename: filename})
			return r.GetFile(), err
		}, func(file *apprunnerv1.StorageEntry, err error) {
			d.finish(err, fmt.Sprintf("%s downloaded to %s/%s", file.GetName(), root, directory))
		})
		return
	}
	paths := append([]string(nil), d.paths...)
	d.progress.SetRange(0, 100)
	d.progress.SetValue(0)
	d.progress.Show()
	call(func(context.Context) ([]uploadedFile, error) {
		return d.mw.client.upload(ctx, root, directory, paths, func(sent, total int64) {
			mainthread.Start(func() {
				if total > 0 {
					d.progress.SetValue(int(sent * 100 / total))
				}
			})
		})
	}, func(uploaded []uploadedFile, err error) {
		d.finish(err, fmt.Sprintf("%s uploaded to %s/%s", pluralize(len(uploaded), "file", "files"), root, directory))
	})
}

func (d *storageDialog) finish(err error, notice string) {
	d.busy = false
	d.cancelFn = nil
	d.progress.Hide()
	d.update()
	if err != nil {
		if context.Canceled == err || strings.Contains(err.Error(), "context canceled") {
			return
		}
		d.errors.show(messageError, escape(errorMessage(err)))
		return
	}
	d.mw.showNotice(strings.TrimSuffix(notice, "/"))
	d.mw.refresh()
	d.Accept()
}
