// Command argui is a Qt desktop interface for an App Runner server. It uses
// the same Twirp API as the web interface and arcli.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mappu/miqt/qt6"
)

const defaultServer = "http://127.0.0.1:8080"

func main() {
	flags := flag.NewFlagSet("argui", flag.ContinueOnError)
	server := flags.String("server", defaultServer, "App Runner server URL")
	timeout := flags.Duration("timeout", 30*time.Second, "request timeout")
	view := flags.String("view", "vms", "place to open first: vms, interfaces, storage:iso, storage:disk, cloudinit, or bridge:NAME")
	screenshot := flags.String("screenshot", "", "save the main window as PNG once the first refresh completes, then exit")
	if err := flags.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "argui:", err)
		os.Exit(2)
	}

	qt6.NewQApplication(os.Args)
	rememberSystemTheme()
	prefs := loadSettings(settingsPath())
	applyColourScheme(prefs.ColourScheme)

	window := newMainWindow(newClient(*server, *timeout), prefs)
	window.initialView = *view
	window.screenshot = *screenshot
	window.Show()
	os.Exit(qt6.QApplication_Exec())
}
