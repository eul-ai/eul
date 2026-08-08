package tool

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

func parseStreamingJSONObject(raw string) map[string]any {
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.UseNumber()
	var complete map[string]any
	if err := decoder.Decode(&complete); err == nil {
		if complete == nil {
			return map[string]any{}
		}
		var trailing any
		if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
			return complete
		}
		return map[string]any{}
	}

	parser := streamingJSONParser{input: []byte(raw)}
	value, ok := parser.parseObject()
	if !ok {
		return map[string]any{}
	}
	return value
}

type streamingJSONParser struct {
	input []byte
	pos   int
}

func (p *streamingJSONParser) parseObject() (map[string]any, bool) {
	p.skipSpace()
	if !p.take('{') {
		return nil, false
	}

	result := map[string]any{}
	for {
		p.skipSpace()
		if p.done() || p.take('}') {
			return result, true
		}

		key, _, ok := p.parseString()
		if !ok {
			return result, true
		}
		p.skipSpace()
		if !p.take(':') {
			return result, true
		}

		value, ok := p.parseValue()
		if !ok {
			return result, true
		}
		result[key] = value

		p.skipSpace()
		switch {
		case p.take(','):
			continue
		case p.take('}'), p.done():
			return result, true
		default:
			return result, true
		}
	}
}

func (p *streamingJSONParser) parseArray() ([]any, bool) {
	p.skipSpace()
	if !p.take('[') {
		return nil, false
	}

	var result []any
	for {
		p.skipSpace()
		if p.done() || p.take(']') {
			return result, true
		}

		value, ok := p.parseValue()
		if !ok {
			return result, true
		}
		result = append(result, value)

		p.skipSpace()
		switch {
		case p.take(','):
			continue
		case p.take(']'), p.done():
			return result, true
		default:
			return result, true
		}
	}
}

func (p *streamingJSONParser) parseValue() (any, bool) {
	p.skipSpace()
	if p.done() {
		return nil, false
	}

	switch p.input[p.pos] {
	case '"':
		value, _, ok := p.parseString()
		return value, ok
	case '{':
		return p.parseObject()
	case '[':
		return p.parseArray()
	case 't':
		return p.parseLiteral("true", true)
	case 'f':
		return p.parseLiteral("false", false)
	case 'n':
		return p.parseLiteral("null", nil)
	default:
		return p.parseNumber()
	}
}

func (p *streamingJSONParser) parseLiteral(token string, value any) (any, bool) {
	if len(p.input)-p.pos < len(token) || string(p.input[p.pos:p.pos+len(token)]) != token {
		p.pos = len(p.input)
		return nil, false
	}
	p.pos += len(token)
	return value, true
}

func (p *streamingJSONParser) parseNumber() (any, bool) {
	start := p.pos
	for !p.done() {
		character := p.input[p.pos]
		if character != '-' && character != '+' && character != '.' && character != 'e' && character != 'E' && (character < '0' || character > '9') {
			break
		}
		p.pos++
	}
	if start == p.pos {
		return nil, false
	}

	token := string(p.input[start:p.pos])
	var value json.Number
	if err := json.Unmarshal([]byte(token), &value); err != nil {
		return nil, false
	}
	return value, true
}

func (p *streamingJSONParser) parseString() (string, bool, bool) {
	p.skipSpace()
	if !p.take('"') {
		return "", false, false
	}

	var output bytes.Buffer
	for !p.done() {
		character := p.input[p.pos]
		p.pos++
		switch character {
		case '"':
			return output.String(), true, true
		case '\\':
			if !p.appendEscape(&output) {
				return output.String(), false, true
			}
		default:
			if character < 0x20 {
				continue
			}
			output.WriteByte(character)
		}
	}
	return output.String(), false, true
}

func (p *streamingJSONParser) appendEscape(output *bytes.Buffer) bool {
	if p.done() {
		return false
	}

	escape := p.input[p.pos]
	p.pos++
	switch escape {
	case '"', '\\', '/':
		output.WriteByte(escape)
	case 'b':
		output.WriteByte('\b')
	case 'f':
		output.WriteByte('\f')
	case 'n':
		output.WriteByte('\n')
	case 'r':
		output.WriteByte('\r')
	case 't':
		output.WriteByte('\t')
	case 'u':
		first, ok := p.takeUnicodeEscape()
		if !ok {
			return false
		}
		if utf16.IsSurrogate(first) {
			if len(p.input)-p.pos < 6 || p.input[p.pos] != '\\' || p.input[p.pos+1] != 'u' {
				return true
			}
			position := p.pos
			p.pos += 2
			second, secondOK := p.takeUnicodeEscape()
			if secondOK {
				decoded := utf16.DecodeRune(first, second)
				if decoded != utf8.RuneError {
					output.WriteRune(decoded)
					return true
				}
			}
			p.pos = position
			return true
		}
		output.WriteRune(first)
	default:
		output.WriteByte(escape)
	}
	return true
}

func (p *streamingJSONParser) takeUnicodeEscape() (rune, bool) {
	if len(p.input)-p.pos < 4 {
		p.pos = len(p.input)
		return 0, false
	}
	value, err := strconv.ParseUint(string(p.input[p.pos:p.pos+4]), 16, 16)
	if err != nil {
		return 0, false
	}
	p.pos += 4
	return rune(value), true
}

func (p *streamingJSONParser) skipSpace() {
	for !p.done() {
		switch p.input[p.pos] {
		case ' ', '\n', '\r', '\t':
			p.pos++
		default:
			return
		}
	}
}

func (p *streamingJSONParser) take(character byte) bool {
	if p.done() || p.input[p.pos] != character {
		return false
	}
	p.pos++
	return true
}

func (p *streamingJSONParser) done() bool {
	return p.pos >= len(p.input)
}
