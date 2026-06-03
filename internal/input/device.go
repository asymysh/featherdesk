package input

import (
	"encoding/binary"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	uinputPath = "/dev/uinput"

	uiSetEvbit  = 0x40045564
	uiSetKeybit = 0x40045565
	uiSetRelbit = 0x40045566
	uiSetAbsbit = 0x40045567
	uiDevCreate = 0x5501
	uiDevDestroy = 0x5502

	evSyn = 0x00
	evKey = 0x01
	evRel = 0x02
	evAbs = 0x03

	synReport = 0x00

	absX = 0x00
	absY = 0x01

	relWheel  = 0x08
	relHWheel = 0x06

	btnLeft   = 0x110
	btnRight  = 0x111
	btnMiddle = 0x112
)

type inputEvent struct {
	Sec   int64
	Usec  int64
	Type  uint16
	Code  uint16
	Value int32
}

const inputEventSize = 24

type uinputUserDev struct {
	Name       [80]byte
	ID         inputID
	EffectsMax uint32
	Absmax     [64]int32
	Absmin     [64]int32
	Absfuzz    [64]int32
	Absflat    [64]int32
}

type inputID struct {
	Bustype uint16
	Vendor  uint16
	Product uint16
	Version uint16
}

type Device struct {
	fd     *os.File
	width  int
	height int
}

func NewDevice(width, height int) (*Device, error) {
	f, err := os.OpenFile(uinputPath, os.O_WRONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("input: open uinput: %w", err)
	}

	fd := int(f.Fd())

	if err := ioctl(fd, uiSetEvbit, evKey); err != nil {
		f.Close()
		return nil, fmt.Errorf("input: set EV_KEY: %w", err)
	}
	if err := ioctl(fd, uiSetEvbit, evRel); err != nil {
		f.Close()
		return nil, fmt.Errorf("input: set EV_REL: %w", err)
	}
	if err := ioctl(fd, uiSetEvbit, evAbs); err != nil {
		f.Close()
		return nil, fmt.Errorf("input: set EV_ABS: %w", err)
	}
	if err := ioctl(fd, uiSetEvbit, evSyn); err != nil {
		f.Close()
		return nil, fmt.Errorf("input: set EV_SYN: %w", err)
	}

	// Register all keyboard keys (0..255)
	for i := 0; i < 256; i++ {
		ioctl(fd, uiSetKeybit, i)
	}
	// Mouse buttons
	ioctl(fd, uiSetKeybit, btnLeft)
	ioctl(fd, uiSetKeybit, btnRight)
	ioctl(fd, uiSetKeybit, btnMiddle)

	// Absolute axes
	ioctl(fd, uiSetAbsbit, absX)
	ioctl(fd, uiSetAbsbit, absY)

	// Relative axes (wheel)
	ioctl(fd, uiSetRelbit, relWheel)
	ioctl(fd, uiSetRelbit, relHWheel)

	// Set up device struct
	var dev uinputUserDev
	copy(dev.Name[:], "viewport-rds")
	dev.ID.Bustype = 0x03 // BUS_USB
	dev.ID.Vendor = 0x1234
	dev.ID.Product = 0x5678
	dev.ID.Version = 1
	dev.Absmax[absX] = int32(width - 1)
	dev.Absmax[absY] = int32(height - 1)
	dev.Absmin[absX] = 0
	dev.Absmin[absY] = 0

	devBytes := (*[unsafe.Sizeof(dev)]byte)(unsafe.Pointer(&dev))[:]
	if _, err := f.Write(devBytes); err != nil {
		f.Close()
		return nil, fmt.Errorf("input: write uinput_user_dev: %w", err)
	}

	if err := ioctl(fd, uiDevCreate, 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("input: UI_DEV_CREATE: %w", err)
	}

	return &Device{fd: f, width: width, height: height}, nil
}

func (d *Device) InjectKey(code uint16, down bool) error {
	val := int32(0)
	if down {
		val = 1
	}
	if err := d.writeEvent(evKey, code, val); err != nil {
		return err
	}
	return d.writeEvent(evSyn, synReport, 0)
}

func (d *Device) InjectMouseMove(x, y int) error {
	if err := d.writeEvent(evAbs, absX, int32(x)); err != nil {
		return err
	}
	if err := d.writeEvent(evAbs, absY, int32(y)); err != nil {
		return err
	}
	return d.writeEvent(evSyn, synReport, 0)
}

func (d *Device) InjectMouseButton(button uint16, down bool) error {
	val := int32(0)
	if down {
		val = 1
	}
	if err := d.writeEvent(evKey, button, val); err != nil {
		return err
	}
	return d.writeEvent(evSyn, synReport, 0)
}

func (d *Device) InjectWheel(delta int32) error {
	if err := d.writeEvent(evRel, relWheel, delta); err != nil {
		return err
	}
	return d.writeEvent(evSyn, synReport, 0)
}

func (d *Device) Close() error {
	ioctl(int(d.fd.Fd()), uiDevDestroy, 0)
	return d.fd.Close()
}

func (d *Device) writeEvent(typ uint16, code uint16, value int32) error {
	var buf [inputEventSize]byte
	// timeval is zeroed (kernel fills it)
	binary.LittleEndian.PutUint16(buf[16:], typ)
	binary.LittleEndian.PutUint16(buf[18:], code)
	binary.LittleEndian.PutUint32(buf[20:], uint32(value))
	_, err := d.fd.Write(buf[:])
	return err
}

func ioctl(fd int, req uint, val int) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(req), uintptr(val))
	if errno != 0 {
		return errno
	}
	return nil
}
