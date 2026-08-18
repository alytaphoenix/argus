package ui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Up          key.Binding
	Down        key.Binding
	Enter       key.Binding
	Back        key.Binding
	Start       key.Binding
	Stop        key.Binding
	Reboot      key.Binding
	Refresh     key.Binding
	Quit        key.Binding
	SaltApply   key.Binding
	OpenSalt    key.Binding
	OpenNomad   key.Binding
	SSH         key.Binding
	Clone       key.Binding
	Credentials key.Binding
	Select      key.Binding
	Help        key.Binding
}

var keys = keyMap{
	Up: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	),
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "open"),
	),
	Back: key.NewBinding(
		key.WithKeys("esc", "b"),
		key.WithHelp("esc/b", "back"),
	),
	Start: key.NewBinding(
		key.WithKeys("s"),
		key.WithHelp("s", "start"),
	),
	Stop: key.NewBinding(
		key.WithKeys("S"),
		key.WithHelp("S", "stop"),
	),
	Reboot: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "reboot"),
	),
	Refresh: key.NewBinding(
		key.WithKeys("R"),
		key.WithHelp("R", "refresh"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
	SaltApply: key.NewBinding(
		key.WithKeys("a"),
		key.WithHelp("a", "apply highstate"),
	),
	OpenSalt: key.NewBinding(
		key.WithKeys("m"),
		key.WithHelp("m", "minions"),
	),
	OpenNomad: key.NewBinding(
		key.WithKeys("n"),
		key.WithHelp("n", "nomad"),
	),
	SSH: key.NewBinding(
		key.WithKeys("t"),
		key.WithHelp("t", "ssh"),
	),
	Clone: key.NewBinding(
		key.WithKeys("c"),
		key.WithHelp("c", "clone/provision"),
	),
	Credentials: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "keys"),
	),
	Select: key.NewBinding(
		key.WithKeys(" "),
		key.WithHelp("space", "select"),
	),
	Help: key.NewBinding(
		key.WithKeys("H"),
		key.WithHelp("H", "help"),
	),
}

func nodeHelpStr(hasSalt, hasNomad bool) string {
	base := "↑/k up  ↓/j down  enter open"
	if hasSalt {
		base += "  m minions"
	}
	if hasNomad {
		base += "  n nomad"
	}
	return styleHelp.Render(base + "  p profiles  ? keys  R refresh  q quit")
}

func guestHelpStr() string {
	return styleHelp.Render("↑/k up  ↓/j down  space select  enter detail  s start  S stop  r reboot  t ssh  c provision  z right-size  R refresh  b back  q quit")
}

func detailHelpStr() string {
	return styleHelp.Render("s start  S stop  r reboot  t ssh  R refresh  b back  q quit")
}

func saltHelpStr() string {
	return styleHelp.Render("↑/k up  ↓/j down  enter detail  a apply highstate  R refresh  b back  q quit")
}

func saltDetailHelpStr() string {
	return styleHelp.Render("a highstate  A custom state  p pick profile  R refresh  b back  q quit")
}

func nomadHelpStr() string {
	return styleHelp.Render("↑/k up  ↓/j down  enter detail  S stop  R refresh  b back  q quit")
}

func nomadDetailHelpStr() string {
	return styleHelp.Render("S stop  R refresh  b back  q quit")
}
