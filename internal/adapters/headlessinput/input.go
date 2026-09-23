// Package headlessinput converts scripted text into keyboard events.
package headlessinput

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/bnema/nefertty/internal/adapters/xkb"
	"github.com/bnema/nefertty/internal/ports"
	"github.com/bnema/zerowrap"
)

// Run owns the keymap and converts script lines into input events.
func Run(ctx context.Context, km *xkb.Keymap, script <-chan string, input chan<- ports.InputEvent, log zerowrap.Logger) error {
	defer km.Close()
	emit := func(code uint32, down bool) error {
		ev := km.Key(code, down, uint32(time.Now().UnixMilli()))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case input <- ev:
		}
		timer := time.NewTimer(2 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	stroke := func(name string, mods []uint32) error {
		code, shift, ok := km.KeycodeFor(name)
		if !ok {
			log.Warn().Str("key", name).Msg("unknown key")
			return nil
		}
		if shift {
			mods = append(mods, 42)
		}
		for _, m := range mods {
			if err := emit(m, true); err != nil {
				return err
			}
		}
		if err := emit(code, true); err != nil {
			return err
		}
		if err := emit(code, false); err != nil {
			return err
		}
		for i := len(mods) - 1; i >= 0; i-- {
			if err := emit(mods[i], false); err != nil {
				return err
			}
		}
		return nil
	}
	punctuation := map[rune]string{' ': "space", '\n': "Return", '.': "period", ',': "comma", '-': "minus", '_': "underscore", '/': "slash", ':': "colon", ';': "semicolon", '!': "exclam", '?': "question", '\'': "apostrophe", '"': "quotedbl", '(': "parenleft", ')': "parenright", '=': "equal", '+': "plus", '\\': "backslash", '@': "at", '#': "numbersign", '$': "dollar", '%': "percent", '&': "ampersand", '*': "asterisk", '[': "bracketleft", ']': "bracketright", '{': "braceleft", '}': "braceright", '<': "less", '>': "greater", '|': "bar", '`': "grave", '~': "asciitilde", '^': "asciicircum"}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case line, ok := <-script:
			if !ok {
				return nil
			}
			switch {
			case strings.HasPrefix(line, "type "):
				for _, r := range strings.TrimPrefix(line, "type ") {
					name := punctuation[r]
					if name == "" && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
						name = string(r)
					}
					if name == "" {
						log.Warn().Str("rune", string(r)).Msg("unknown rune")
						continue
					}
					if err := stroke(name, nil); err != nil {
						return err
					}
				}
			case strings.HasPrefix(line, "key "):
				parts := strings.Split(strings.TrimPrefix(line, "key "), "+")
				var mods []uint32
				valid := true
				for _, part := range parts[:len(parts)-1] {
					switch part {
					case "Super":
						mods = append(mods, 125)
					case "Shift":
						mods = append(mods, 42)
					case "Ctrl":
						mods = append(mods, 29)
					case "Alt":
						mods = append(mods, 56)
					default:
						valid = false
					}
				}
				if !valid {
					log.Warn().Str("combo", line).Msg("unknown modifier")
					continue
				}
				if err := stroke(parts[len(parts)-1], mods); err != nil {
					return err
				}
			case strings.HasPrefix(line, "sleep "):
				d, err := time.ParseDuration(strings.TrimPrefix(line, "sleep "))
				if err != nil || d < 0 {
					log.Warn().Str("line", line).Msg("invalid sleep")
					continue
				}
				timer := time.NewTimer(d)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			default:
				log.Warn().Str("line", line).Msg("unknown script line")
			}
		}
	}
}
