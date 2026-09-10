// Package ui implements the interactive terminal dashboard.
package ui

import (
	"sort"
	"strings"

	"github.com/c0m4r/tui-chart/internal/chart"
)

// Theme is the complete color scheme of the dashboard.
type Theme struct {
	Name string
	// Chrome
	Bg       chart.Color // page background
	PanelBg  chart.Color // panel interior
	HeaderBg chart.Color
	FooterBg chart.Color
	Border   chart.Color
	BorderHi chart.Color // border of the focused panel
	Fg       chart.Color
	Muted    chart.Color
	Faint    chart.Color
	Accent   chart.Color
	Good     chart.Color
	Warn     chart.Color
	Bad      chart.Color
	// Tab styling
	TabActiveFg   chart.Color
	TabActiveBg   chart.Color
	TabInactiveFg chart.Color
	TabHoverBg    chart.Color
	// Selection highlight in the sidebar
	SelectBg chart.Color
	// Stripe is the alternating row background in tables
	Stripe chart.Color
}

// ChartPalette adapts a theme for the chart engine.
func (t Theme) ChartPalette() chart.Palette {
	return chart.Palette{
		BG:    t.PanelBg,
		FG:    t.Fg,
		Muted: t.Muted,
		Grid:  t.Faint,
	}
}

func hex(s string) chart.Color { return chart.MustHex(s) }

// Themes is the registry of built in themes, in presentation order.
var Themes = map[string]Theme{
	"midnight": {
		Name:          "midnight",
		Bg:            hex("#0b0e14"),
		PanelBg:       hex("#0f131b"),
		HeaderBg:      hex("#151b26"),
		FooterBg:      hex("#151b26"),
		Border:        hex("#232b3a"),
		BorderHi:      hex("#3d6ea8"),
		Fg:            hex("#ccd3e0"),
		Muted:         hex("#7c8798"),
		Faint:         hex("#2c3444"),
		Accent:        hex("#5ab0f5"),
		Good:          hex("#4ade80"),
		Warn:          hex("#fbbf24"),
		Bad:           hex("#f87171"),
		TabActiveFg:   hex("#0b0e14"),
		TabActiveBg:   hex("#5ab0f5"),
		TabInactiveFg: hex("#8b95a7"),
		TabHoverBg:    hex("#1b2230"),
		SelectBg:      hex("#18202e"),
		Stripe:        hex("#131924"),
	},
	"nord": {
		Name:          "nord",
		Bg:            hex("#2e3440"),
		PanelBg:       hex("#292e39"),
		HeaderBg:      hex("#3b4252"),
		FooterBg:      hex("#3b4252"),
		Border:        hex("#434c5e"),
		BorderHi:      hex("#88c0d0"),
		Fg:            hex("#e5e9f0"),
		Muted:         hex("#9aa5b5"),
		Faint:         hex("#3d4553"),
		Accent:        hex("#88c0d0"),
		Good:          hex("#a3be8c"),
		Warn:          hex("#ebcb8b"),
		Bad:           hex("#bf616a"),
		TabActiveFg:   hex("#2e3440"),
		TabActiveBg:   hex("#88c0d0"),
		TabInactiveFg: hex("#a3adbb"),
		TabHoverBg:    hex("#353b49"),
		SelectBg:      hex("#333a47"),
		Stripe:        hex("#303643"),
	},
	"gruvbox": {
		Name:          "gruvbox",
		Bg:            hex("#1d2021"),
		PanelBg:       hex("#1d2021"),
		HeaderBg:      hex("#32302f"),
		FooterBg:      hex("#32302f"),
		Border:        hex("#3c3836"),
		BorderHi:      hex("#d79921"),
		Fg:            hex("#ebdbb2"),
		Muted:         hex("#a89984"),
		Faint:         hex("#3c3836"),
		Accent:        hex("#fabd2f"),
		Good:          hex("#b8bb26"),
		Warn:          hex("#fe8019"),
		Bad:           hex("#fb4934"),
		TabActiveFg:   hex("#1d2021"),
		TabActiveBg:   hex("#fabd2f"),
		TabInactiveFg: hex("#a89984"),
		TabHoverBg:    hex("#282828"),
		SelectBg:      hex("#2b2a27"),
		Stripe:        hex("#292929"),
	},
	"dracula": {
		Name:          "dracula",
		Bg:            hex("#1e1f29"),
		PanelBg:       hex("#21222c"),
		HeaderBg:      hex("#282a36"),
		FooterBg:      hex("#282a36"),
		Border:        hex("#3b3d4d"),
		BorderHi:      hex("#bd93f9"),
		Fg:            hex("#f8f8f2"),
		Muted:         hex("#8b90a5"),
		Faint:         hex("#343746"),
		Accent:        hex("#bd93f9"),
		Good:          hex("#50fa7b"),
		Warn:          hex("#f1fa8c"),
		Bad:           hex("#ff5555"),
		TabActiveFg:   hex("#21222c"),
		TabActiveBg:   hex("#bd93f9"),
		TabInactiveFg: hex("#8b90a5"),
		TabHoverBg:    hex("#2c2e3b"),
		SelectBg:      hex("#2a2c39"),
		Stripe:        hex("#282a36"),
	},
	"light": {
		Name:          "light",
		Bg:            hex("#f7f8fa"),
		PanelBg:       hex("#ffffff"),
		HeaderBg:      hex("#e8ebf0"),
		FooterBg:      hex("#e8ebf0"),
		Border:        hex("#c9d0da"),
		BorderHi:      hex("#2f6feb"),
		Fg:            hex("#1f2430"),
		Muted:         hex("#5c6675"),
		Faint:         hex("#dde2ea"),
		Accent:        hex("#2f6feb"),
		Good:          hex("#0f9d58"),
		Warn:          hex("#c77700"),
		Bad:           hex("#d93025"),
		TabActiveFg:   hex("#ffffff"),
		TabActiveBg:   hex("#2f6feb"),
		TabInactiveFg: hex("#5c6675"),
		TabHoverBg:    hex("#dfe4ec"),
		SelectBg:      hex("#eef1f6"),
		Stripe:        hex("#f0f3f8"),
	},
	"mono": {
		Name:          "mono",
		Bg:            hex("#0a0a0a"),
		PanelBg:       hex("#0d0d0d"),
		HeaderBg:      hex("#1a1a1a"),
		FooterBg:      hex("#1a1a1a"),
		Border:        hex("#333333"),
		BorderHi:      hex("#dddddd"),
		Fg:            hex("#e6e6e6"),
		Muted:         hex("#9a9a9a"),
		Faint:         hex("#2b2b2b"),
		Accent:        hex("#ffffff"),
		Good:          hex("#e6e6e6"),
		Warn:          hex("#c0c0c0"),
		Bad:           hex("#ffffff"),
		TabActiveFg:   hex("#0a0a0a"),
		TabActiveBg:   hex("#e6e6e6"),
		TabInactiveFg: hex("#9a9a9a"),
		TabHoverBg:    hex("#1f1f1f"),
		SelectBg:      hex("#191919"),
		Stripe:        hex("#141414"),
	},
}

// ThemeNames lists the built in themes alphabetically.
func ThemeNames() []string {
	names := make([]string, 0, len(Themes))
	for name := range Themes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// LookupTheme resolves a theme name case insensitively, falling back to
// midnight.
func LookupTheme(name string) (Theme, bool) {
	if name == "" {
		return Themes["midnight"], true
	}
	if t, ok := Themes[strings.ToLower(strings.TrimSpace(name))]; ok {
		return t, true
	}
	return Themes["midnight"], false
}

// shortenName builds a fixed width label for a tab.
func shortenName(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}
