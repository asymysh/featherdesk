package input

import "encoding/json"

type InputMessage struct {
	Type   string `json:"type"`
	Event  string `json:"event,omitempty"`
	Code   string `json:"code,omitempty"`
	Button int    `json:"button,omitempty"`
	X      int    `json:"x,omitempty"`
	Y      int    `json:"y,omitempty"`
	DeltaY int    `json:"deltaY,omitempty"`
}

func ParseMessage(data []byte) (*InputMessage, error) {
	var msg InputMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func (d *Device) HandleMessage(msg *InputMessage) {
	switch msg.Type {
	case "key":
		code, ok := BrowserCodeToLinux(msg.Code)
		if !ok {
			return
		}
		d.InjectKey(code, msg.Event == "down")

	case "mousemove":
		d.InjectMouseMove(msg.X, msg.Y)

	case "mousedown":
		btn := MouseButtonToLinux(msg.Button)
		d.InjectMouseButton(btn, true)

	case "mouseup":
		btn := MouseButtonToLinux(msg.Button)
		d.InjectMouseButton(btn, false)

	case "wheel":
		delta := int32(msg.DeltaY)
		if delta > 0 {
			d.InjectWheel(-1)
		} else if delta < 0 {
			d.InjectWheel(1)
		}
	}
}
