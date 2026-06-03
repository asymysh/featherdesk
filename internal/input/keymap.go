package input

var browserToLinux = map[string]uint16{
	"KeyA": 30, "KeyB": 48, "KeyC": 46, "KeyD": 32, "KeyE": 18,
	"KeyF": 33, "KeyG": 34, "KeyH": 35, "KeyI": 23, "KeyJ": 36,
	"KeyK": 37, "KeyL": 38, "KeyM": 50, "KeyN": 49, "KeyO": 24,
	"KeyP": 25, "KeyQ": 16, "KeyR": 19, "KeyS": 31, "KeyT": 20,
	"KeyU": 22, "KeyV": 47, "KeyW": 17, "KeyX": 45, "KeyY": 21,
	"KeyZ": 44,

	"Digit0": 11, "Digit1": 2, "Digit2": 3, "Digit3": 4, "Digit4": 5,
	"Digit5": 6, "Digit6": 7, "Digit7": 8, "Digit8": 9, "Digit9": 10,

	"F1": 59, "F2": 60, "F3": 61, "F4": 62, "F5": 63, "F6": 64,
	"F7": 65, "F8": 66, "F9": 67, "F10": 68, "F11": 87, "F12": 88,

	"ShiftLeft": 42, "ShiftRight": 54,
	"ControlLeft": 29, "ControlRight": 97,
	"AltLeft": 56, "AltRight": 100,
	"MetaLeft": 125, "MetaRight": 126,

	"ArrowUp": 103, "ArrowDown": 108, "ArrowLeft": 105, "ArrowRight": 106,
	"Home": 102, "End": 107, "PageUp": 104, "PageDown": 109,

	"Backspace": 14, "Tab": 15, "Enter": 28, "Escape": 1,
	"Delete": 111, "Insert": 110, "Space": 57,

	"Semicolon": 39, "Equal": 13, "Comma": 51, "Minus": 12,
	"Period": 52, "Slash": 53, "Backquote": 41,
	"BracketLeft": 26, "BracketRight": 27, "Backslash": 43, "Quote": 40,

	"CapsLock": 58, "NumLock": 69, "ScrollLock": 70,
	"PrintScreen": 99, "Pause": 119,
	"ContextMenu": 127,
}

func BrowserCodeToLinux(code string) (uint16, bool) {
	v, ok := browserToLinux[code]
	return v, ok
}

func MouseButtonToLinux(button int) uint16 {
	switch button {
	case 0:
		return btnLeft
	case 1:
		return btnMiddle
	case 2:
		return btnRight
	default:
		return btnLeft
	}
}
