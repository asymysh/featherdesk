package protocol

import (
	"encoding/binary"
	"errors"
)

const HeaderSize = 17

const (
	FrameTypeVideoH264 uint8 = 1
	FrameTypePing      uint8 = 2
	FrameTypePong      uint8 = 3
	FrameTypeAudioPCM  uint8 = 4
)

type FrameHeader struct {
	Type        uint8
	Timestamp   uint64
	Width       uint16
	Height      uint16
	PayloadSize uint32
}

var errShortBuffer = errors.New("protocol: buffer too short for header")

func MarshalHeader(h FrameHeader, buf []byte) {
	buf[0] = h.Type
	binary.LittleEndian.PutUint64(buf[1:9], h.Timestamp)
	binary.LittleEndian.PutUint16(buf[9:11], h.Width)
	binary.LittleEndian.PutUint16(buf[11:13], h.Height)
	binary.LittleEndian.PutUint32(buf[13:17], h.PayloadSize)
}

func UnmarshalHeader(buf []byte) (FrameHeader, error) {
	if len(buf) < HeaderSize {
		return FrameHeader{}, errShortBuffer
	}
	return FrameHeader{
		Type:        buf[0],
		Timestamp:   binary.LittleEndian.Uint64(buf[1:9]),
		Width:       binary.LittleEndian.Uint16(buf[9:11]),
		Height:      binary.LittleEndian.Uint16(buf[11:13]),
		PayloadSize: binary.LittleEndian.Uint32(buf[13:17]),
	}, nil
}
