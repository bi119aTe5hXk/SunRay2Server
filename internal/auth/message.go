// SPDX-License-Identifier: GPL-2.0-or-later

package auth

import (
	"fmt"
	"strings"
)

// Message is one line of the Sun Ray authentication protocol.
type Message struct {
	Type   string
	Fields map[string]string
}

func Parse(line string) (Message, error) {
	parts := strings.Fields(strings.TrimSpace(line))
	if len(parts) == 0 {
		return Message{}, fmt.Errorf("empty authentication message")
	}

	m := Message{Type: parts[0], Fields: make(map[string]string, len(parts)-1)}
	for _, part := range parts[1:] {
		key, value, ok := strings.Cut(part, "=")
		if !ok || key == "" {
			return Message{}, fmt.Errorf("invalid authentication field %q", part)
		}
		m.Fields[key] = value
	}
	return m, nil
}

func (m Message) Get(key string) string {
	return m.Fields[key]
}

func InfoResponse(tokenSeq string) string {
	return "connInf useReal=true encUpType=none tokenSeq=" + tokenSeq +
		" module=StartSession.m3 access=allowed token=pseudo.00212839eff9 encDownType=none"
}
