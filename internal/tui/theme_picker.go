package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// themePrefs is the content of the theme file.
type themePrefs struct {
	Theme string `json:"theme"`
}

// DefaultThemeFile is where the TUI remembers the chosen theme, next to the
// saved remote, so local and remote sessions share it.
func DefaultThemeFile() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "up", "tui.json")
}

// loadTheme reads the saved theme name. A missing, unreadable or malformed
// file means the default theme; it is never an error.
func loadTheme(file string) string {
	if file == "" {
		return themes[0].name
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return themes[0].name
	}
	var prefs themePrefs
	if json.Unmarshal(data, &prefs) != nil {
		return themes[0].name
	}
	return themes[themeIndex(prefs.Theme)].name
}

// saveTheme replaces the theme file atomically.
func saveTheme(file, name string) error {
	if file == "" {
		return errors.New("cannot find the config directory")
	}
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "tui.json.tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = json.NewEncoder(f).Encode(themePrefs{Theme: name}); err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err == nil {
		err = os.Rename(f.Name(), file)
	}
	return err
}

// themePicker previews the highlighted theme while open; previous is the
// theme restored by esc.
type themePicker struct {
	cursor   int
	previous string
}

type themeSavedMsg struct {
	name string
	err  error
}

func (m Model) openThemePicker() Model {
	m.themePicker = &themePicker{cursor: themeIndex(m.theme), previous: m.theme}
	return m
}

func (m Model) handleThemeKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := *m.themePicker
	switch msg.String() {
	case "up", "k":
		p.cursor = (p.cursor + len(themes) - 1) % len(themes)
	case "down", "j":
		p.cursor = (p.cursor + 1) % len(themes)
	case "esc":
		m.theme = applyTheme(p.previous)
		m.themePicker = nil
		return m, nil
	case "enter":
		m.theme = themes[p.cursor].name
		m.themePicker = nil
		file, name := m.themeFile, m.theme
		return m, func() tea.Msg { return themeSavedMsg{name: name, err: saveTheme(file, name)} }
	default:
		return m, nil
	}
	applyTheme(themes[p.cursor].name)
	m.themePicker = &p
	return m, nil
}

func (m Model) themeSaved(msg themeSavedMsg) Model {
	if msg.err != nil {
		m.notice, m.noticeOK = "Cannot save theme: "+msg.err.Error(), false
		return m
	}
	m.notice, m.noticeOK = "✓ Theme: "+themes[themeIndex(msg.name)].title, true
	return m
}

// renderThemePicker lists every theme with a swatch of its own colors, so each
// one can be compared without selecting it.
func renderThemePicker(p themePicker) string {
	lines := []string{titleStyle.Render("Theme")}
	for i, t := range themes {
		marker := "  "
		name := t.title
		if i == p.cursor {
			marker = accentStyle.Render("› ")
			name = selectedNameStyle.Render(name)
		}
		var swatch strings.Builder
		for _, c := range []lipgloss.Style{
			lipgloss.NewStyle().Foreground(t.accent),
			lipgloss.NewStyle().Foreground(t.up),
			lipgloss.NewStyle().Foreground(t.warn),
			lipgloss.NewStyle().Foreground(t.down),
			lipgloss.NewStyle().Foreground(t.dim),
		} {
			swatch.WriteString(c.Render("●"))
		}
		lines = append(lines, fmt.Sprintf("%s%s%s%s", marker, name, strings.Repeat(" ", 13-ansi.StringWidth(t.title)), swatch.String()))
	}
	lines = append(lines, "", renderHelp("↑/↓ choose · enter save · esc cancel"))
	return strings.Join(lines, "\n")
}
