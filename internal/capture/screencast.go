package capture

import (
	"fmt"
	"time"

	"github.com/godbus/dbus/v5"
)

func getMutterScreencastNode() (uint32, *dbus.Conn, dbus.ObjectPath, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return 0, nil, "", fmt.Errorf("capture: dbus session connect: %v", err)
	}

	mutter := conn.Object("org.gnome.Mutter.ScreenCast", "/org/gnome/Mutter/ScreenCast")

	var sessionPath dbus.ObjectPath
	err = mutter.Call("org.gnome.Mutter.ScreenCast.CreateSession", 0, map[string]dbus.Variant{}).Store(&sessionPath)
	if err != nil {
		conn.Close()
		return 0, nil, "", fmt.Errorf("capture: CreateSession: %v", err)
	}

	session := conn.Object("org.gnome.Mutter.ScreenCast", sessionPath)

	var streamPath dbus.ObjectPath
	err = session.Call("org.gnome.Mutter.ScreenCast.Session.RecordMonitor", 0, "", map[string]dbus.Variant{}).Store(&streamPath)
	if err != nil {
		conn.Close()
		return 0, nil, "", fmt.Errorf("capture: RecordMonitor: %v", err)
	}

	sigCh := make(chan *dbus.Signal, 1)
	conn.Signal(sigCh)
	conn.AddMatchSignal(
		dbus.WithMatchObjectPath(streamPath),
		dbus.WithMatchInterface("org.gnome.Mutter.ScreenCast.Stream"),
		dbus.WithMatchMember("PipeWireStreamAdded"),
	)

	err = session.Call("org.gnome.Mutter.ScreenCast.Session.Start", 0).Err
	if err != nil {
		conn.Close()
		return 0, nil, "", fmt.Errorf("capture: Session.Start: %v", err)
	}

	select {
	case sig := <-sigCh:
		if len(sig.Body) > 0 {
			nodeID, ok := sig.Body[0].(uint32)
			if !ok {
				conn.Close()
				return 0, nil, "", fmt.Errorf("capture: unexpected node_id type: %T", sig.Body[0])
			}
			return nodeID, conn, sessionPath, nil
		}
		conn.Close()
		return 0, nil, "", fmt.Errorf("capture: PipeWireStreamAdded signal empty")
	case <-time.After(5 * time.Second):
		conn.Close()
		return 0, nil, "", fmt.Errorf("capture: timeout waiting for PipeWireStreamAdded")
	}
}

func stopMutterSession(conn *dbus.Conn, sessionPath dbus.ObjectPath) {
	if conn == nil {
		return
	}
	session := conn.Object("org.gnome.Mutter.ScreenCast", sessionPath)
	session.Call("org.gnome.Mutter.ScreenCast.Session.Stop", 0)
	conn.Close()
}
