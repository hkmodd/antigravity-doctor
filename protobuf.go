package main

// Protobuf varint encoding/decoding — minimal, zero dependencies

func encodeVarint(v uint64) []byte {
	var buf []byte
	for v > 0x7F {
		buf = append(buf, byte(v&0x7F)|0x80)
		v >>= 7
	}
	buf = append(buf, byte(v&0x7F))
	if len(buf) == 0 {
		return []byte{0}
	}
	return buf
}

func decodeVarint(data []byte, pos int) (uint64, int) {
	var result uint64
	var shift uint
	for pos < len(data) {
		b := data[pos]
		result |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			return result, pos + 1
		}
		shift += 7
		pos++
	}
	return result, pos
}

func skipProtoField(data []byte, pos int, wireType uint64) int {
	switch wireType {
	case 0: // varint
		_, pos = decodeVarint(data, pos)
	case 2: // length-delimited
		length, newPos := decodeVarint(data, pos)
		pos = newPos + int(length)
	case 1: // 64-bit
		pos += 8
	case 5: // 32-bit
		pos += 4
	}
	return pos
}

func stripField(data []byte, targetField uint64) []byte {
	var remaining []byte
	pos := 0
	for pos < len(data) {
		start := pos
		tag, newPos := decodeVarint(data, pos)
		if newPos == pos {
			remaining = append(remaining, data[start:]...)
			break
		}
		pos = newPos
		wireType := tag & 7
		fieldNum := tag >> 3

		endPos := skipProtoField(data, pos, wireType)
		if endPos == pos && wireType != 0 && wireType != 1 && wireType != 2 && wireType != 5 {
			remaining = append(remaining, data[start:]...)
			break
		}
		pos = endPos

		if fieldNum != targetField {
			remaining = append(remaining, data[start:pos]...)
		}
	}
	return remaining
}

func encodeLengthDelimited(fieldNumber uint64, data []byte) []byte {
	tag := (fieldNumber << 3) | 2
	result := encodeVarint(tag)
	result = append(result, encodeVarint(uint64(len(data)))...)
	result = append(result, data...)
	return result
}

func encodeStringField(fieldNumber uint64, s string) []byte {
	return encodeLengthDelimited(fieldNumber, []byte(s))
}

func encodeTimestampFields(epoch float64) []byte {
	sec := uint64(epoch)
	inner := encodeVarint((1 << 3) | 0)
	inner = append(inner, encodeVarint(sec)...)
	var result []byte
	result = append(result, encodeLengthDelimited(3, inner)...)
	result = append(result, encodeLengthDelimited(7, inner)...)
	result = append(result, encodeLengthDelimited(10, inner)...)
	return result
}

func hasTimestampFields(blob []byte) bool {
	if len(blob) == 0 {
		return false
	}
	pos := 0
	for pos < len(blob) {
		tag, newPos := decodeVarint(blob, pos)
		if newPos == pos {
			break
		}
		pos = newPos
		fn := tag >> 3
		wt := tag & 7
		if fn == 3 || fn == 7 || fn == 10 {
			return true
		}
		pos = skipProtoField(blob, pos, wt)
	}
	return false
}
