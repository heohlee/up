package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func fg(hex string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(hex)).Render("x")
}

func themedModel(t *testing.T, file string) Model {
	t.Helper()
	m := New(nil, func() time.Time { return now }, Options{ThemeFile: file})
	updated, _ := m.Update(snapshotMsg{snap: seededSnapshot(t, now)})
	return updated.(Model)
}

func TestDefaultThemeKeepsThePalette(t *testing.T) {
	themedModel(t, filepath.Join(t.TempDir(), "missing", "tui.json"))
	for style, hex := range map[*lipgloss.Style]string{&accentStyle: "#a78bfa", &helpStyle: "#808080", &upStyle: "#22c55e", &slowStyle: "#f59e0b"} {
		if got, want := style.Render("x"), fg(hex); got != want {
			t.Errorf("style renders %q, want %q", got, want)
		}
	}
	if got, want := downStyle.Render("x"), lipgloss.NewStyle().Foreground(lipgloss.Color("#ef4444")).Bold(true).Render("x"); got != want {
		t.Errorf("down renders %q, want %q", got, want)
	}
}

func TestThemePickerPreviewsSavesAndReloads(t *testing.T) {
	file := filepath.Join(t.TempDir(), "up", "tui.json")
	m := themedModel(t, file)
	m, _ = press(t, m, keyT)
	screen := view(m)
	for _, want := range []string{"Theme", "Default", "Dracula", "Nord", "Gruvbox", "Catppuccin", "Tokyo Night", "One Dark", "Solarized", "Monokai", "Rosé Pine", "Kanagawa", "Everforest", "Ayu", "enter save", "esc cancel"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("picker lacks %q:\n%s", want, screen)
		}
	}
	m, _ = press(t, m, keyDown, keyDown)
	if got, want := accentStyle.Render("x"), fg("#88c0d0"); got != want {
		t.Fatalf("preview accent = %q, want nord %q", got, want)
	}
	if _, cmd := press(t, m, keyEnter); cmd == nil {
		t.Fatal("enter returned no save command")
	}
}

func TestThemeSaveWritesFileAndNextRunUsesIt(t *testing.T) {
	file := filepath.Join(t.TempDir(), "up", "tui.json")
	m := themedModel(t, file)
	m, _ = press(t, m, keyT, keyDown)
	m, cmd := press(t, m, keyEnter)
	if cmd == nil {
		t.Fatal("enter returned no save command")
	}
	next, _ := m.Update(cmd())
	m = next.(Model)
	if strings.Contains(view(m), "Catppuccin") {
		t.Fatal("picker still open after enter")
	}
	if !strings.Contains(view(m), "✓ Theme: Dracula") {
		t.Fatalf("no confirmation:\n%s", view(m))
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != `{"theme":"dracula"}` {
		t.Fatalf("file = %s", got)
	}
	themedModel(t, filepath.Join(t.TempDir(), "other.json"))
	if accentStyle.Render("x") != fg("#a78bfa") {
		t.Fatal("a model without the file did not reset to default")
	}
	themedModel(t, file)
	if got, want := accentStyle.Render("x"), fg("#bd93f9"); got != want {
		t.Fatalf("reloaded accent = %q, want dracula %q", got, want)
	}
}

func TestThemeEscRestoresAndSavesNothing(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tui.json")
	m := themedModel(t, file)
	m, _ = press(t, m, keyT, keyDown, keyDown, keyDown)
	m, cmd := press(t, m, keyEsc)
	if cmd != nil {
		t.Fatal("esc returned a command")
	}
	if accentStyle.Render("x") != fg("#a78bfa") {
		t.Fatal("esc did not restore the default theme")
	}
	if strings.Contains(view(m), "Gruvbox") {
		t.Fatal("picker still open after esc")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("esc wrote the file: %v", err)
	}
}

func TestThemeFileUnknownOrMalformedFallsBackToDefault(t *testing.T) {
	for _, content := range []string{`{"theme":"vaporwave"}`, `not json`, ``} {
		file := filepath.Join(t.TempDir(), "tui.json")
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		themedModel(t, filepath.Join(t.TempDir(), "x.json"))
		applyTheme("nord")
		themedModel(t, file)
		if accentStyle.Render("x") != fg("#a78bfa") {
			t.Errorf("%q: not the default theme", content)
		}
	}
}

func TestThemeSaveFailureKeepsThemeAndWarns(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m := themedModel(t, filepath.Join(blocker, "tui.json"))
	m, _ = press(t, m, keyT, keyDown)
	m, cmd := press(t, m, keyEnter)
	next, quit := m.Update(cmd())
	m = next.(Model)
	if quit != nil {
		t.Fatal("save failure returned a command")
	}
	if !strings.Contains(view(m), "Cannot save theme:") {
		t.Fatalf("no warning:\n%s", view(m))
	}
	if accentStyle.Render("x") != fg("#bd93f9") {
		t.Fatal("save failure dropped the chosen theme")
	}
}

func TestHelpListsThemeKey(t *testing.T) {
	for want, text := range map[string]string{"t        theme": renderHelpPopup(false), "t   theme": renderKeys()} {
		if !strings.Contains(ansi.Strip(text), want) {
			t.Errorf("help lacks %q:\n%s", want, ansi.Strip(text))
		}
	}
}

func TestEveryThemeIsDistinctAndSelectable(t *testing.T) {
	seen := map[string]string{}
	for _, p := range themes {
		key := fmt.Sprint(p.accent, p.dim, p.up, p.down, p.warn)
		if other, ok := seen[key]; ok {
			t.Errorf("%s repeats the palette of %s", p.name, other)
		}
		seen[key] = p.name
	}
	file := filepath.Join(t.TempDir(), "tui.json")
	m := themedModel(t, file)
	m, cmd := press(t, m, keyT, keyUp, keyEnter)
	next, _ := m.Update(cmd())
	if !strings.Contains(view(next.(Model)), "✓ Theme: Ayu") {
		t.Fatalf("up from Default did not wrap to the last theme:\n%s", view(next.(Model)))
	}
	themedModel(t, file)
	if got, want := accentStyle.Render("x"), fg("#39bae6"); got != want {
		t.Fatalf("reloaded accent = %q, want ayu %q", got, want)
	}
}

func TestDividersUseThePopupBorderColor(t *testing.T) {
	for _, name := range []string{"default", "nord"} {
		applyTheme(name)
		border := accentStyle
		if got, want := renderRule(20, -1), border.Render(logoStem)+border.Render(strings.Repeat("─", 19)); got != want {
			t.Errorf("%s rule = %q, want %q", name, got, want)
		}
		if got := renderHostRuleCells(Snapshot{}, "host", now, 20, -1); !strings.Contains(got, border.Render("─")) || !strings.Contains(got, helpStyle.Render(" host ")) {
			t.Errorf("%s host rule = %q", name, got)
		}
		m := themedModel(t, filepath.Join(t.TempDir(), "t.json"))
		applyTheme(name)
		next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		screen := next.(Model).View().Content
		color, _, _ := strings.Cut(border.Render("x"), "x")
		for _, want := range []string{border.Render(" │ "), color + strings.Repeat("─", 40)} {
			if !strings.Contains(screen, want) {
				t.Errorf("%s split view lacks divider %q", name, want)
			}
		}
		if !strings.HasPrefix(renderHelpPopup(false), color+"╭─ keys ") {
			t.Errorf("%s popup border changed color", name)
		}
	}
	applyTheme("default")
}
