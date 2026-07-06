package grokquota

import (
	"encoding/binary"
	"fmt"
)

type protoField struct {
	Number uint64
	Wire   uint64
	Varint uint64
	Bytes  []byte
}

func parseProtoFields(data []byte) ([]protoField, error) {
	fields := make([]protoField, 0)
	for offset := 0; offset < len(data); {
		tag, next, err := readProtoVarint(data, offset)
		if err != nil {
			return nil, err
		}
		offset = next
		field := protoField{Number: tag >> 3, Wire: tag & 7}
		switch field.Wire {
		case 0:
			value, n, err := readProtoVarint(data, offset)
			if err != nil {
				return nil, err
			}
			field.Varint = value
			offset = n
		case 1:
			if offset+8 > len(data) {
				return nil, fmt.Errorf("truncated fixed64 field %d", field.Number)
			}
			field.Bytes = append([]byte(nil), data[offset:offset+8]...)
			offset += 8
		case 2:
			length, n, err := readProtoVarint(data, offset)
			if err != nil {
				return nil, err
			}
			offset = n
			if length > uint64(len(data)-offset) {
				return nil, fmt.Errorf("truncated length-delimited field %d", field.Number)
			}
			field.Bytes = append([]byte(nil), data[offset:offset+int(length)]...)
			offset += int(length)
		case 5:
			if offset+4 > len(data) {
				return nil, fmt.Errorf("truncated fixed32 field %d", field.Number)
			}
			field.Bytes = append([]byte(nil), data[offset:offset+4]...)
			field.Varint = uint64(binary.LittleEndian.Uint32(field.Bytes))
			offset += 4
		default:
			return nil, fmt.Errorf("unsupported wire type %d for field %d", field.Wire, field.Number)
		}
		fields = append(fields, field)
	}
	return fields, nil
}

func readProtoVarint(data []byte, offset int) (uint64, int, error) {
	var value uint64
	var shift uint
	for i := offset; i < len(data); i++ {
		b := data[i]
		value |= uint64(b&0x7f) << shift
		if b < 0x80 {
			return value, i + 1, nil
		}
		shift += 7
		if shift >= 64 {
			return 0, i + 1, fmt.Errorf("varint overflow at %d", offset)
		}
	}
	return 0, len(data), fmt.Errorf("truncated varint at %d", offset)
}

func firstField(fields []protoField, number uint64) *protoField {
	for i := range fields {
		if fields[i].Number == number {
			return &fields[i]
		}
	}
	return nil
}
