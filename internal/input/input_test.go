package input

import "testing"

func TestParseMessage(t *testing.T) {
	tests := []struct {
		name  string
		json  string
		mtype string
	}{
		{"key down", `{"type":"key","event":"down","code":"KeyA"}`, "key"},
		{"key up", `{"type":"key","event":"up","code":"ShiftLeft"}`, "key"},
		{"mousemove", `{"type":"mousemove","x":100,"y":200}`, "mousemove"},
		{"mousedown", `{"type":"mousedown","button":0}`, "mousedown"},
		{"mouseup", `{"type":"mouseup","button":2}`, "mouseup"},
		{"wheel", `{"type":"wheel","deltaY":-120}`, "wheel"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := ParseMessage([]byte(tt.json))
			if err != nil {
				t.Fatalf("ParseMessage: %v", err)
			}
			if msg.Type != tt.mtype {
				t.Fatalf("type: got %q, want %q", msg.Type, tt.mtype)
			}
		})
	}
}

func TestParseMessageInvalid(t *testing.T) {
	_, err := ParseMessage([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestBrowserCodeToLinux(t *testing.T) {
	tests := []struct {
		code string
		want uint16
	}{
		{"KeyA", 30},
		{"Space", 57},
		{"Enter", 28},
		{"ArrowUp", 103},
		{"ShiftLeft", 42},
		{"F1", 59},
	}
	for _, tt := range tests {
		got, ok := BrowserCodeToLinux(tt.code)
		if !ok {
			t.Fatalf("BrowserCodeToLinux(%q): not found", tt.code)
		}
		if got != tt.want {
			t.Fatalf("BrowserCodeToLinux(%q): got %d, want %d", tt.code, got, tt.want)
		}
	}

	_, ok := BrowserCodeToLinux("NonExistent")
	if ok {
		t.Fatal("expected false for unknown code")
	}
}

func TestMouseButtonToLinux(t *testing.T) {
	if MouseButtonToLinux(0) != btnLeft {
		t.Fatal("button 0 should be BTN_LEFT")
	}
	if MouseButtonToLinux(1) != btnMiddle {
		t.Fatal("button 1 should be BTN_MIDDLE")
	}
	if MouseButtonToLinux(2) != btnRight {
		t.Fatal("button 2 should be BTN_RIGHT")
	}
}
