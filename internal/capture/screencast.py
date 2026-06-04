#!/usr/bin/env python3
"""Captures the GNOME screen via Mutter ScreenCast D-Bus + PipeWire and outputs raw RGBA frames to stdout."""
import sys
import signal
import subprocess
import dbus
from dbus.mainloop.glib import DBusGMainLoop
from gi.repository import GLib

DBusGMainLoop(set_as_default=True)

def get_screencast_stream():
    bus = dbus.SessionBus()
    mutter = bus.get_object('org.gnome.Mutter.ScreenCast', '/org/gnome/Mutter/ScreenCast')
    sc = dbus.Interface(mutter, 'org.gnome.Mutter.ScreenCast')

    session_path = sc.CreateSession({})
    session_obj = bus.get_object('org.gnome.Mutter.ScreenCast', session_path)
    session = dbus.Interface(session_obj, 'org.gnome.Mutter.ScreenCast.Session')

    stream_path = session.RecordMonitor('', {})
    stream_obj = bus.get_object('org.gnome.Mutter.ScreenCast', stream_path)

    loop = GLib.MainLoop()
    node_id_holder = [None]

    def on_pipewire_stream_added(node_id):
        node_id_holder[0] = int(node_id)
        loop.quit()

    stream_obj.connect_to_signal('PipeWireStreamAdded', on_pipewire_stream_added,
                                  dbus_interface='org.gnome.Mutter.ScreenCast.Stream')

    session.Start()
    GLib.timeout_add(5000, loop.quit)
    loop.run()

    return node_id_holder[0], session

if __name__ == '__main__':
    fps = int(sys.argv[1]) if len(sys.argv) > 1 else 30
    node_id, session = get_screencast_stream()
    if node_id is None:
        print("Failed to get PipeWire node ID", file=sys.stderr)
        sys.exit(1)
    print(f"PipeWire node: {node_id}", file=sys.stderr)

    cmd = [
        'gst-launch-1.0', '-q',
        'pipewiresrc', f'path={node_id}', 'do-timestamp=true',
        'keepalive-time=100', 'resend-last=true', '!',
        'queue', 'max-size-buffers=1', 'max-size-time=0', 'max-size-bytes=0', 'leaky=downstream', '!',
        'videoconvert', '!',
        'video/x-raw,format=RGBA', '!',
        'fdsink', 'fd=1', 'sync=false'
    ]

    proc = subprocess.Popen(cmd, stdout=sys.stdout.buffer)

    def cleanup(signum=None, frame=None):
        proc.terminate()
        try:
            session.Stop()
        except Exception:
            pass
        sys.exit(0)

    signal.signal(signal.SIGTERM, cleanup)
    signal.signal(signal.SIGINT, cleanup)

    try:
        proc.wait()
    except KeyboardInterrupt:
        cleanup()
