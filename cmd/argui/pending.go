package main

import (
	"fmt"
	"time"

	"github.com/mappu/miqt/qt6"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
)

// pendingBanner shows a network change awaiting confirmation with its
// rollback countdown.
type pendingBanner struct {
	*messageWidget
	mw       *mainWindow
	progress *qt6.QProgressBar
	timer    *qt6.QTimer
	change   *apprunnerv1.PendingNetworkChange
}

func newPendingBanner(mw *mainWindow) *pendingBanner {
	p := &pendingBanner{messageWidget: newMessageWidget(false), mw: mw}
	p.progress = qt6.NewQProgressBar2()
	p.progress.SetRange(0, 150)
	p.progress.SetTextVisible(false)
	p.progress.SetFixedHeight(5)
	p.progress.SetStyleSheet(fmt.Sprintf("QProgressBar { background: rgba(0,0,0,0.35); border: none; border-radius: 2px; } QProgressBar::chunk { background: %s; border-radius: 2px; }", colorNeutral))
	layout := p.Layout()
	_ = layout
	p.timer = qt6.NewQTimer2(mw.QObject)
	p.timer.SetInterval(200)
	p.timer.OnTimeout(func() { p.tick() })
	return p
}

func (p *pendingBanner) update(change *apprunnerv1.PendingNetworkChange) {
	p.change = change
	if change == nil {
		p.timer.Stop()
		p.Hide()
		return
	}
	p.setActions(
		bannerAction{label: "Keep Changes", handler: func() { p.mw.confirmNetworkChange(change.GetId()) }},
		bannerAction{label: "Revert Now", handler: func() { p.mw.revertNetworkChange(change.GetId()) }},
	)
	p.tick()
	p.timer.Start2()
}

func (p *pendingBanner) tick() {
	if p.change == nil {
		return
	}
	remaining := formatCountdown(p.change.GetExpiresAt(), time.Now())
	p.show(messageWarning, fmt.Sprintf("<b>%s</b><br>Backend rollback in %s unless this client confirms connectivity.", escape(p.change.GetDescription()), pluralize(remaining, "second", "seconds")))
	for _, button := range p.buttons {
		if button.Text() == "Keep Changes" {
			button.SetEnabled(remaining > 0 && !p.mw.busy)
		} else {
			button.SetEnabled(!p.mw.busy)
		}
	}
}
