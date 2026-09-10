package main

import (
	"fmt"

	"github.com/mappu/miqt/qt6"
)

// Breeze colour scheme values, used for semantic colours in both schemes.
const (
	colorPositive  = "#27ae60"
	colorNeutral   = "#f67400"
	colorNegative  = "#da4453"
	colorHighlight = "#3daee9"
	colorInactive  = "#7f8c8d"
)

var (
	systemPalette   *qt6.QPalette
	systemStyleName string
	iconCache       = map[string]*qt6.QIcon{}
)

func rememberSystemTheme() {
	systemPalette = qt6.NewQPalette()
	systemStyleName = qt6.QApplication_Style().ObjectName()
}

func applyColourScheme(scheme colourScheme) {
	switch scheme {
	case schemeDark:
		qt6.QApplication_SetStyle(qt6.QStyleFactory_Create("Fusion"))
		qt6.QApplication_SetPalette(breezeDarkPalette())
	case schemeLight:
		qt6.QApplication_SetStyle(qt6.QStyleFactory_Create("Fusion"))
		qt6.QApplication_SetPalette(breezeLightPalette())
	default:
		if systemStyleName != "" {
			if style := qt6.QStyleFactory_Create(systemStyleName); style != nil {
				qt6.QApplication_SetStyle(style)
			}
		}
		if systemPalette != nil {
			qt6.QApplication_SetPalette(systemPalette)
		}
	}
	iconCache = map[string]*qt6.QIcon{}
}

func color(hex string) *qt6.QColor {
	return qt6.NewQColor6(hex)
}

func breezeDarkPalette() *qt6.QPalette {
	p := qt6.NewQPalette()
	set := func(role qt6.QPalette__ColorRole, hex string) { p.SetColor2(role, color(hex)) }
	set(qt6.QPalette__Window, "#2a2e32")
	set(qt6.QPalette__WindowText, "#fcfcfc")
	set(qt6.QPalette__Base, "#1b1e20")
	set(qt6.QPalette__AlternateBase, "#232629")
	set(qt6.QPalette__Text, "#fcfcfc")
	set(qt6.QPalette__Button, "#31363b")
	set(qt6.QPalette__ButtonText, "#fcfcfc")
	set(qt6.QPalette__BrightText, "#ffffff")
	set(qt6.QPalette__Highlight, colorHighlight)
	set(qt6.QPalette__HighlightedText, "#fcfcfc")
	set(qt6.QPalette__Link, colorHighlight)
	set(qt6.QPalette__ToolTipBase, "#31363b")
	set(qt6.QPalette__ToolTipText, "#fcfcfc")
	set(qt6.QPalette__PlaceholderText, colorInactive)
	set(qt6.QPalette__Light, "#4d5257")
	set(qt6.QPalette__Midlight, "#3b4045")
	set(qt6.QPalette__Mid, colorInactive)
	set(qt6.QPalette__Dark, "#1b1e20")
	set(qt6.QPalette__Shadow, "#101214")
	for _, role := range []qt6.QPalette__ColorRole{qt6.QPalette__WindowText, qt6.QPalette__Text, qt6.QPalette__ButtonText} {
		p.SetColor(qt6.QPalette__Disabled, role, color(colorInactive))
	}
	p.SetColor(qt6.QPalette__Disabled, qt6.QPalette__Highlight, color("#3b4045"))
	return p
}

func breezeLightPalette() *qt6.QPalette {
	p := qt6.NewQPalette()
	set := func(role qt6.QPalette__ColorRole, hex string) { p.SetColor2(role, color(hex)) }
	set(qt6.QPalette__Window, "#eff0f1")
	set(qt6.QPalette__WindowText, "#232629")
	set(qt6.QPalette__Base, "#fcfcfc")
	set(qt6.QPalette__AlternateBase, "#eff0f1")
	set(qt6.QPalette__Text, "#232629")
	set(qt6.QPalette__Button, "#fcfcfc")
	set(qt6.QPalette__ButtonText, "#232629")
	set(qt6.QPalette__BrightText, "#ffffff")
	set(qt6.QPalette__Highlight, colorHighlight)
	set(qt6.QPalette__HighlightedText, "#fcfcfc")
	set(qt6.QPalette__Link, "#2980b9")
	set(qt6.QPalette__ToolTipBase, "#fcfcfc")
	set(qt6.QPalette__ToolTipText, "#232629")
	set(qt6.QPalette__PlaceholderText, "#a0a3a5")
	set(qt6.QPalette__Light, "#ffffff")
	set(qt6.QPalette__Midlight, "#e3e5e7")
	set(qt6.QPalette__Mid, "#6e7175")
	set(qt6.QPalette__Dark, "#b6b9bc")
	set(qt6.QPalette__Shadow, "#8f9295")
	for _, role := range []qt6.QPalette__ColorRole{qt6.QPalette__WindowText, qt6.QPalette__Text, qt6.QPalette__ButtonText} {
		p.SetColor(qt6.QPalette__Disabled, role, color("#a0a3a5"))
	}
	return p
}

// themeIcon returns a freedesktop theme icon, or the style's standard icon
// when the theme lacks it.
func themeIcon(name string, fallback qt6.QStyle__StandardPixmap) *qt6.QIcon {
	key := fmt.Sprintf("theme:%s:%d", name, fallback)
	if icon, ok := iconCache[key]; ok {
		return icon
	}
	icon := qt6.QIcon_FromTheme(name)
	if icon.IsNull() && fallback != 0 {
		icon = qt6.QApplication_Style().StandardIcon(fallback, nil, nil)
	}
	iconCache[key] = icon
	return icon
}

// dotIcon draws a small filled circle used as a status indicator.
func dotIcon(hex string) *qt6.QIcon {
	key := "dot:" + hex
	if icon, ok := iconCache[key]; ok {
		return icon
	}
	const size = 16
	image := qt6.NewQImage3(size, size, qt6.QImage__Format_ARGB32)
	image.Fill(0)
	painter := qt6.NewQPainter2(image.QPaintDevice)
	painter.SetRenderHint(qt6.QPainter__Antialiasing)
	painter.SetPen(qt6.NewQColor2(qt6.Transparent))
	painter.SetBrush(qt6.NewQBrush3(color(hex)))
	painter.DrawEllipse2(4, 4, 8, 8)
	painter.End()
	icon := qt6.NewQIcon2(qt6.QPixmap_FromImage(image))
	iconCache[key] = icon
	return icon
}

// dotHTML renders a coloured status dot for rich-text labels.
func dotHTML(hex string) string {
	return fmt.Sprintf(`<span style="color:%s">&#9679;</span>`, hex)
}

func monospaceFont() *qt6.QFont {
	font := qt6.NewQFont2("monospace")
	font.SetStyleHint(qt6.QFont__Monospace)
	font.SetFixedPitch(true)
	return font
}
