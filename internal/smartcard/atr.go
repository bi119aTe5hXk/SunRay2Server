// SPDX-License-Identifier: GPL-2.0-or-later

package smartcard

import (
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
)

var ErrNeedMore = errors.New("need more ATR bytes")

// ATR is an ISO/IEC 7816 Answer To Reset found in a smart-card byte stream.
type ATR struct {
	Raw        []byte
	Direct     bool
	Protocols  []int
	Historical []byte
}

func (a ATR) Hex() string {
	return strings.ToUpper(hex.EncodeToString(a.Raw))
}

func (a ATR) HistoricalHex() string {
	return strings.ToUpper(hex.EncodeToString(a.Historical))
}

func (a ATR) ProtocolNames() string {
	parts := make([]string, 0, len(a.Protocols))
	for _, protocol := range a.Protocols {
		parts = append(parts, fmt.Sprintf("T=%d", protocol))
	}
	return strings.Join(parts, ",")
}

// ParseATR parses one ATR at the beginning of data and returns its byte length.
// It sends no command to the card and interprets only ISO 7816-3 framing.
func ParseATR(data []byte) (ATR, int, error) {
	if len(data) < 2 {
		return ATR{}, 0, ErrNeedMore
	}
	if data[0] != 0x3B && data[0] != 0x3F {
		return ATR{}, 0, fmt.Errorf("invalid initial character 0x%02X", data[0])
	}

	t0 := data[1]
	historicalLength := int(t0 & 0x0F)
	y := t0 >> 4
	offset := 2
	var protocols []int
	hasNonT0 := false

	for groups := 0; ; groups++ {
		if groups > 7 {
			return ATR{}, 0, fmt.Errorf("too many interface byte groups")
		}
		for mask := byte(0x1); mask <= 0x4; mask <<= 1 {
			if y&mask == 0 {
				continue
			}
			if offset >= len(data) {
				return ATR{}, 0, ErrNeedMore
			}
			offset++
		}
		if y&0x8 == 0 {
			break
		}
		if offset >= len(data) {
			return ATR{}, 0, ErrNeedMore
		}
		td := data[offset]
		offset++
		protocol := int(td & 0x0F)
		if !slices.Contains(protocols, protocol) {
			protocols = append(protocols, protocol)
		}
		if protocol != 0 {
			hasNonT0 = true
		}
		y = td >> 4
	}
	if len(protocols) == 0 {
		protocols = []int{0}
	}

	total := offset + historicalLength
	if hasNonT0 {
		total++
	}
	if total > 33 {
		return ATR{}, 0, fmt.Errorf("ATR exceeds ISO 7816 maximum: %d bytes", total)
	}
	if len(data) < total {
		return ATR{}, 0, ErrNeedMore
	}
	if hasNonT0 {
		var checksum byte
		for _, value := range data[1:total] {
			checksum ^= value
		}
		if checksum != 0 {
			return ATR{}, 0, fmt.Errorf("invalid TCK checksum")
		}
	}
	historicalStart := offset
	atr := ATR{
		Raw:        append([]byte(nil), data[:total]...),
		Direct:     data[0] == 0x3B,
		Protocols:  protocols,
		Historical: append([]byte(nil), data[historicalStart:historicalStart+historicalLength]...),
	}
	return atr, total, nil
}

// FindATRs scans framed or unframed probe traffic for valid ATR byte strings.
func FindATRs(data []byte) []ATR {
	var found []ATR
	for offset := 0; offset < len(data); offset++ {
		if data[offset] != 0x3B && data[offset] != 0x3F {
			continue
		}
		atr, consumed, err := ParseATR(data[offset:])
		if err != nil {
			continue
		}
		found = append(found, atr)
		offset += consumed - 1
	}
	return found
}
