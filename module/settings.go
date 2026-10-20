package module

// Setting is one of a module's settings, shown with the module in the
// control panel and changed there, live, like the core's own. When it
// changes, the module is set up again with the new value.
type Setting struct {
	// Key names it within the module: lowercase letters, digits and
	// dashes.
	Key string
	// Label and Hint are shown in the control panel.
	Label string
	Hint  string
	// Choices, when set, make it a choice among these values, the first
	// being the default. Without them, it's a switch, "off" by default,
	// and its value is "on" or "off".
	Choices []Choice
}

// Choice is one value a setting can take.
type Choice struct {
	Value string
	Label string
}
