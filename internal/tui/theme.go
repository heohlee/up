package tui

import (
	"image/color"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"charm.land/lipgloss/v2"
)

const (
	logoMark    = "▲"
	logoStem    = "└"
	productName = "up"
)

// palette is a theme: one foreground color per role. The terminal background
// is always kept.
type palette struct {
	name, title                 string
	accent, dim, up, down, warn color.Color
}

func newPalette(name, title, accent, dim, up, down, warn string) palette {
	return palette{name, title, lipgloss.Color(accent), lipgloss.Color(dim), lipgloss.Color(up), lipgloss.Color(down), lipgloss.Color(warn)}
}

// All palette choices live here; the first one is the default. The others use
// the public colors of their namesake schemes.
var themes = []palette{
	newPalette("default", "Default", "#a78bfa", "#808080", "#22c55e", "#ef4444", "#f59e0b"),
	newPalette("dracula", "Dracula", "#bd93f9", "#6272a4", "#50fa7b", "#ff5555", "#ffb86c"),
	newPalette("nord", "Nord", "#88c0d0", "#616e88", "#a3be8c", "#bf616a", "#ebcb8b"),
	newPalette("gruvbox", "Gruvbox", "#83a598", "#928374", "#b8bb26", "#fb4934", "#fabd2f"),
	newPalette("catppuccin", "Catppuccin", "#cba6f7", "#7f849c", "#a6e3a1", "#f38ba8", "#fab387"),
}

// Styles are shared values rebuilt only by applyTheme, which the model calls
// when it starts and when the theme picker changes the theme.
var (
	theme             palette
	accentStyle       lipgloss.Style
	titleStyle        lipgloss.Style
	selectedNameStyle = lipgloss.NewStyle().Bold(true)
	helpStyle         lipgloss.Style
	headerStyle       lipgloss.Style
	upStyle           lipgloss.Style
	downStyle         lipgloss.Style
	slowStyle         lipgloss.Style
	pendingStyle      lipgloss.Style
	warnStyle         lipgloss.Style
)

func init() { applyTheme(themes[0].name) }

// themeIndex is the position of the named theme, or 0 (default) when unknown.
func themeIndex(name string) int {
	for i, p := range themes {
		if p.name == name {
			return i
		}
	}
	return 0
}

// applyTheme rebuilds every style from the named palette and returns the name
// actually applied: unknown names select the default.
func applyTheme(name string) string {
	theme = themes[themeIndex(name)]
	accentStyle = lipgloss.NewStyle().Foreground(theme.accent)
	titleStyle = accentStyle.Bold(true)
	helpStyle = lipgloss.NewStyle().Foreground(theme.dim)
	headerStyle = helpStyle.Bold(true)
	upStyle = lipgloss.NewStyle().Foreground(theme.up)
	downStyle = lipgloss.NewStyle().Foreground(theme.down).Bold(true)
	slowStyle = lipgloss.NewStyle().Foreground(theme.warn)
	pendingStyle = helpStyle
	warnStyle = lipgloss.NewStyle().Foreground(theme.warn).Bold(true)
	return theme.name
}

func renderHelp(text string) string {
	keys := map[string]bool{"↑/↓": true, "(k/j)": true, "enter": true, "n": true, "e": true, "d": true, "t": true, "space": true, "w": true, "m": true, "u": true, "s": true, "c": true, "a": true, "q": true, "ctrl+c": true, "esc": true, "tab": true, "tab/shift+tab": true, "←/→": true, "y": true, "v": true, "?": true}
	words := strings.Split(text, " ")
	for i, word := range words {
		if keys[word] {
			words[i] = accentStyle.Render(word)
		} else {
			words[i] = helpStyle.Render(word)
		}
	}
	return strings.Join(words, " ")
}

func ruleWidth(width int) int {
	if width <= 0 {
		width = 60
	}
	return min(80, max(20, width))
}

// A negative position renders the original, byte-identical idle rule.
// The pulse replaces cells rather than adding them, clipping at the edge.
func renderRule(width, position int) string {
	return renderRuleCells(ruleWidth(width), position)
}

func renderRuleCells(cells, position int) string {
	length := cells - 1
	stem := accentStyle.Render(logoStem)
	if position < 0 || position >= length {
		return stem + helpStyle.Render(strings.Repeat("─", length))
	}
	pulse := []rune("╱╲╱")[:min(3, length-position)]
	return stem + helpStyle.Render(strings.Repeat("─", position)) +
		accentStyle.Render(string(pulse)) + helpStyle.Render(strings.Repeat("─", length-position-len(pulse)))
}

// renderHostRule keeps every label cell intact while the pulse lights it.
// Positions are relative to the first cell after the stem, as in renderRule.
func renderHostRule(snap Snapshot, hostname string, now time.Time, width, position int) string {
	return renderHostRuleCells(snap, hostname, now, ruleWidth(width), position)
}

func renderHostRuleCells(snap Snapshot, hostname string, now time.Time, cells, position int) string {
	hostname = ansi.Truncate(ansi.Strip(strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, hostname)), 24, "…")
	if snap.Stale(now) {
		position = -1
	}
	label := ""
	if hostname != "" {
		label = " " + hostname + " "
	}
	if 2+ansi.StringWidth(label)+3 > cells {
		label = ""
	}
	if label == "" {
		return renderRuleCells(cells, position)
	}
	var b strings.Builder
	b.WriteString(accentStyle.Render(logoStem))
	cell := 1
	write := func(text string, style lipgloss.Style, rule bool) {
		if position < 0 {
			b.WriteString(style.Render(text))
			cell += ansi.StringWidth(text)
			return
		}
		for _, r := range text {
			glyph := string(r)
			w := ansi.StringWidth(glyph)
			s := style
			if position >= 0 && cell < position+4 && cell+w > position+1 {
				s = accentStyle
				if rule {
					glyph = string([]rune("╱╲╱")[cell-position-1])
				}
			}
			b.WriteString(s.Render(glyph))
			cell += w
		}
	}
	write("─", helpStyle, true)
	write(label, helpStyle, false)
	write(strings.Repeat("─", max(0, cells-cell)), helpStyle, true)
	return b.String()
}

func renderHeader(snap Snapshot, width int) string {
	return renderSweepHeader(snap, width, -1)
}

func renderSweepHeader(snap Snapshot, width, position int) string {
	return renderTitle(snap) + "\n" + renderRule(width, position) + "\n"
}

// displayText strips terminal control sequences and line breaks from text that
// comes from outside the program before it is drawn.
func displayText(s string) string {
	return ansi.Strip(strings.Map(func(r rune) rune {
		if r == '\x1b' {
			return r // let ansi.Strip remove the whole sequence
		}
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s))
}
