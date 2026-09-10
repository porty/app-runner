package main

import (
	"regexp"
	"strings"

	"github.com/mappu/miqt/qt6"
	"gopkg.in/yaml.v3"
)

var (
	yamlComment = regexp.MustCompile(`(^|\s)#.*$`)
	yamlKey     = regexp.MustCompile(`^(\s*(?:-\s+)?)([^\s#'"][^:#]*?)(\s*:)(\s|$)`)
	yamlScalar  = regexp.MustCompile(`\b(true|false|yes|no|null|~|-?\d+(?:\.\d+)?)\b`)
	yamlString  = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'(?:[^']|'')*'`)
)

// newYAMLEditor returns a monospace editor with cloud-config colouring.
func newYAMLEditor() *qt6.QPlainTextEdit {
	editor := qt6.NewQPlainTextEdit2()
	editor.SetFont(monospaceFont())
	editor.SetLineWrapMode(qt6.QPlainTextEdit__NoWrap)
	editor.SetTabStopDistance(float64(editor.FontMetrics().HorizontalAdvance("  ")))
	attachYAMLHighlighter(editor)
	return editor
}

func attachYAMLHighlighter(editor *qt6.QPlainTextEdit) *qt6.QSyntaxHighlighter {
	highlighter := qt6.NewQSyntaxHighlighter2(editor.Document())
	keyFormat := qt6.NewQTextCharFormat()
	keyFormat.SetForeground(qt6.NewQBrush3(color(colorHighlight)))
	scalarFormat := qt6.NewQTextCharFormat()
	scalarFormat.SetForeground(qt6.NewQBrush3(color(colorNeutral)))
	stringFormat := qt6.NewQTextCharFormat()
	stringFormat.SetForeground(qt6.NewQBrush3(color(colorPositive)))
	commentFormat := qt6.NewQTextCharFormat()
	commentFormat.SetForeground(qt6.NewQBrush3(color(colorInactive)))
	highlighter.OnHighlightBlock(func(text string) {
		if strings.HasPrefix(text, "#cloud-config") || strings.HasPrefix(text, "#!") {
			highlighter.SetFormat(0, len(text), commentFormat)
			return
		}
		if match := yamlKey.FindStringSubmatchIndex(text); match != nil {
			highlighter.SetFormat(match[4], match[5]-match[4], keyFormat)
			rest := text[match[6]:]
			offset := match[6]
			for _, m := range yamlScalar.FindAllStringIndex(rest, -1) {
				highlighter.SetFormat(offset+m[0], m[1]-m[0], scalarFormat)
			}
			for _, m := range yamlString.FindAllStringIndex(rest, -1) {
				highlighter.SetFormat(offset+m[0], m[1]-m[0], stringFormat)
			}
			if strings.TrimSpace(rest) != "" && !yamlScalar.MatchString(strings.TrimSpace(rest)) && !yamlString.MatchString(rest) && !strings.HasPrefix(strings.TrimSpace(rest), "#") {
				highlighter.SetFormat(offset, len(rest), stringFormat)
			}
		} else if trimmed := strings.TrimLeft(text, " "); strings.HasPrefix(trimmed, "- ") {
			start := len(text) - len(trimmed) + 2
			highlighter.SetFormat(start, len(text)-start, stringFormat)
		}
		if match := yamlComment.FindStringIndex(text); match != nil {
			highlighter.SetFormat(match[0], match[1]-match[0], commentFormat)
		}
	})
	return highlighter
}

// yamlVerdict describes user-data: whether it parses and what it looks like.
type yamlVerdict struct {
	ok      bool
	color   string
	message string
}

func validateUserData(text string) yamlVerdict {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return yamlVerdict{ok: true, color: colorInactive, message: "Empty user-data"}
	}
	if strings.HasPrefix(trimmed, "#!") {
		return yamlVerdict{ok: true, color: colorPositive, message: "Executable user-data script"}
	}
	var document any
	if err := yaml.Unmarshal([]byte(text), &document); err != nil {
		message := strings.TrimPrefix(err.Error(), "yaml: ")
		return yamlVerdict{ok: false, color: colorNegative, message: "YAML error: " + message}
	}
	if strings.HasPrefix(trimmed, "#cloud-config") {
		if _, isMap := document.(map[string]any); !isMap && document != nil {
			return yamlVerdict{ok: false, color: colorNeutral, message: "cloud-config must be a YAML mapping"}
		}
		return yamlVerdict{ok: true, color: colorPositive, message: "Valid cloud-config"}
	}
	return yamlVerdict{ok: true, color: colorNeutral, message: "Valid YAML without a #cloud-config header"}
}
