package ldap

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// BER tags the directory uses. Only the universal and context specific forms
// the three operations need are implemented.
const (
	tagInteger    = 0x02
	tagOctet      = 0x04
	tagNull       = 0x05
	tagEnumerated = 0x0a
	tagBoolean    = 0x01
	tagSequence   = 0x30
	tagSet        = 0x31
)

func tlv(tag byte, content []byte) []byte {
	encoded := make([]byte, 0, len(content)+4)
	encoded = append(encoded, tag)
	encoded = append(encoded, encodeLength(len(content))...)
	encoded = append(encoded, content...)
	return encoded
}

func encodeLength(length int) []byte {
	if length < 0x80 {
		return []byte{byte(length)}
	}
	var digits []byte
	for value := length; value > 0; value >>= 8 {
		digits = append([]byte{byte(value & 0xff)}, digits...)
	}
	return append([]byte{0x80 | byte(len(digits))}, digits...)
}

func octetString(value string) []byte {
	return tlv(tagOctet, []byte(value))
}

func integer(value int) []byte {
	var digits []byte
	for {
		digits = append([]byte{byte(value & 0xff)}, digits...)
		value >>= 8
		if value == 0 {
			break
		}
	}
	if digits[0]&0x80 != 0 {
		digits = append([]byte{0}, digits...)
	}
	return tlv(tagInteger, digits)
}

func enumerated(value int) []byte {
	encoded := integer(value)
	encoded[0] = tagEnumerated
	return encoded
}

func boolean(value bool) []byte {
	if value {
		return tlv(tagBoolean, []byte{0xff})
	}
	return tlv(tagBoolean, []byte{0x00})
}

func sequence(parts ...[]byte) []byte {
	return tlv(tagSequence, concat(parts))
}

func applicationTag(number int, parts ...[]byte) []byte {
	return tlv(byte(0x60+number), concat(parts))
}

func applicationTagValue(number int) byte {
	return byte(0x60 + number)
}

func contextConstructed(number int, parts ...[]byte) []byte {
	return tlv(byte(0xa0+number), concat(parts))
}

func contextPrimitive(number int, content []byte) []byte {
	return tlv(byte(0x80+number), content)
}

func concat(parts [][]byte) []byte {
	length := 0
	for _, part := range parts {
		length += len(part)
	}
	joined := make([]byte, 0, length)
	for _, part := range parts {
		joined = append(joined, part...)
	}
	return joined
}

func message(id int, operation []byte) []byte {
	return sequence(integer(id), operation)
}

// berValue is one decoded TLV.
type berValue struct {
	tag     byte
	content []byte
}

// readMessage reads one LDAPMessage and returns the protocol operation it
// carries.
func readMessage(reader *bufio.Reader) (berValue, error) {
	outer, err := readTLV(reader)
	if err != nil {
		return berValue{}, err
	}
	tag, content, rest, err := parseTLV(outer.content)
	if err != nil {
		return berValue{}, err
	}
	if tag != tagInteger {
		return berValue{}, errors.New("ldap: message has no message id")
	}
	_ = content
	operationTag, operationContent, _, err := parseTLV(rest)
	if err != nil {
		return berValue{}, err
	}
	return berValue{tag: operationTag, content: operationContent}, nil
}

// readTLV reads one outer TLV from the connection.
func readTLV(reader *bufio.Reader) (berValue, error) {
	tag, err := reader.ReadByte()
	if err != nil {
		return berValue{}, err
	}
	length, err := readLength(reader)
	if err != nil {
		return berValue{}, err
	}
	content := make([]byte, length)
	if _, err := io.ReadFull(reader, content); err != nil {
		return berValue{}, err
	}
	return berValue{tag: tag, content: content}, nil
}

func readLength(reader *bufio.Reader) (int, error) {
	first, err := reader.ReadByte()
	if err != nil {
		return 0, err
	}
	if first&0x80 == 0 {
		return int(first), nil
	}
	count := int(first & 0x7f)
	if count == 0 || count > 4 {
		return 0, errors.New("ldap: invalid length")
	}
	digits := make([]byte, count)
	if _, err := io.ReadFull(reader, digits); err != nil {
		return 0, err
	}
	length := 0
	for _, digit := range digits {
		length = length<<8 | int(digit)
	}
	return length, nil
}

// parseTLV splits the first value off a byte slice.
func parseTLV(data []byte) (byte, []byte, []byte, error) {
	if len(data) < 2 {
		return 0, nil, nil, errors.New("ldap: truncated value")
	}
	tag := data[0]
	length, header, err := decodeLength(data[1:])
	if err != nil {
		return 0, nil, nil, err
	}
	start := 1 + header
	if start+length > len(data) {
		return 0, nil, nil, errors.New("ldap: truncated value")
	}
	return tag, data[start : start+length], data[start+length:], nil
}

func decodeLength(data []byte) (int, int, error) {
	if len(data) == 0 {
		return 0, 0, errors.New("ldap: truncated length")
	}
	first := data[0]
	if first&0x80 == 0 {
		return int(first), 1, nil
	}
	count := int(first & 0x7f)
	if count == 0 || count > 4 || len(data) < 1+count {
		return 0, 0, errors.New("ldap: invalid length")
	}
	length := 0
	for _, digit := range data[1 : 1+count] {
		length = length<<8 | int(digit)
	}
	return length, 1 + count, nil
}

func firstEnumerated(content []byte) (int, error) {
	tag, value, _, err := parseTLV(content)
	if err != nil {
		return 0, err
	}
	if tag != tagEnumerated {
		return 0, errors.New("ldap: response has no result code")
	}
	if len(value) == 0 || len(value) > 4 {
		return 0, errors.New("ldap: invalid result code")
	}
	padded := make([]byte, 4)
	copy(padded[4-len(value):], value)
	return int(binary.BigEndian.Uint32(padded)), nil
}

// parseEntry reads a searchResEntry into the identity the API needs.
func parseEntry(config Config, content []byte) (*Identity, error) {
	dnTag, dn, rest, err := parseTLV(content)
	if err != nil || dnTag != tagOctet {
		return nil, errors.New("ldap: entry has no dn")
	}
	attributesTag, attributeContent, _, err := parseTLV(rest)
	if err != nil || attributesTag != tagSequence {
		return nil, errors.New("ldap: entry has no attributes")
	}

	values := make(map[string][]string)
	for len(attributeContent) > 0 {
		attributeTag, attribute, remaining, err := parseTLV(attributeContent)
		if err != nil {
			return nil, err
		}
		attributeContent = remaining
		if attributeTag != tagSequence {
			continue
		}
		nameTag, name, valueSet, err := parseTLV(attribute)
		if err != nil || nameTag != tagOctet {
			continue
		}
		valueTag, value, _, err := parseTLV(valueSet)
		if err != nil || valueTag != tagSet {
			continue
		}
		for len(value) > 0 {
			valueTag, item, remaining, err := parseTLV(value)
			if err != nil {
				return nil, err
			}
			value = remaining
			if valueTag == tagOctet {
				values[string(name)] = append(values[string(name)], string(item))
			}
		}
	}

	nameAttribute := config.NameAttribute
	if nameAttribute == "" {
		nameAttribute = "cn"
	}
	mailAttribute := config.MailAttribute
	if mailAttribute == "" {
		mailAttribute = "mail"
	}
	identity := &Identity{DN: string(dn)}
	if found := values[nameAttribute]; len(found) > 0 {
		identity.Name = found[0]
	}
	if found := values[mailAttribute]; len(found) > 0 {
		identity.Email = found[0]
	}
	if identity.Email == "" {
		identity.Email = identity.DN
	}
	if identity.Name == "" {
		identity.Name = identity.Email
	}
	return identity, nil
}

// describe renders a decoded value for a test failure message.
func describe(value berValue) string {
	return "tag " + strconv.Itoa(int(value.tag)) + " (" + fmt.Sprint(len(value.content)) + " bytes)"
}
