package cli

import (
	"context"
	"flag"
	"strconv"
	"strings"
	"time"
)

// pointerAction is one parsed `neferwl pointer` action.
type pointerAction struct {
	kind     string // abs, move, press, release, click, hold, drag, scroll
	x, y     float64
	button   uint32
	duration time.Duration // hold; 0 means --hold-duration
	steps    int           // scroll steps
	axis     uint32        // scroll: 0 vertical, 1 horizontal
}

// pointerOptions are the tunable timings of `neferwl pointer`.
type pointerOptions struct {
	output        string
	clickDuration time.Duration
	holdDuration  time.Duration
	dragSteps     int
	dragStepDelay time.Duration
}

// evdev button codes (linux/input-event-codes.h).
var pointerButtons = map[string]uint32{
	"left": 0x110, "right": 0x111, "middle": 0x112, "side": 0x113,
	"extra": 0x114, "forward": 0x115, "back": 0x116, "task": 0x117,
	"btn_left": 0x110, "btn_right": 0x111, "btn_middle": 0x112, "btn_side": 0x113,
	"btn_extra": 0x114, "btn_forward": 0x115, "btn_back": 0x116, "btn_task": 0x117,
}

const btnLeft = 0x110

// parseButton reads a button name, BTN_* name or evdev code (decimal or
// 0x hex) in the mouse range.
func parseButton(s string) (uint32, bool) {
	if b, ok := pointerButtons[strings.ToLower(s)]; ok {
		return b, true
	}
	n, err := strconv.ParseUint(s, 0, 32)
	if err != nil || n < 0x110 || n > 0x11f {
		return 0, false
	}
	return uint32(n), true
}

func isButton(s string) bool { _, ok := parseButton(s); return ok }

func parseFloat(s string) (float64, bool) {
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil && v == v && v-v == 0 // finite
}

// parsePointerActions parses the action list, each action with its
// arguments, in order.
func parsePointerActions(args []string) ([]pointerAction, error) {
	if len(args) == 0 {
		return nil, usagef("no action")
	}
	var out []pointerAction
	for i := 0; i < len(args); i++ {
		name := args[i]
		rest := args[i+1:]
		a := pointerAction{kind: name, button: btnLeft}
		need := func(n int, what string) error {
			if len(rest) < n {
				return usagef("%s: expected %s", name, what)
			}
			return nil
		}
		optButton := func(at int) int {
			if len(rest) > at && isButton(rest[at]) {
				a.button, _ = parseButton(rest[at])
				return 1
			}
			return 0
		}
		switch name {
		case "abs", "move", "drag":
			if err := need(2, "two numbers"); err != nil {
				return nil, err
			}
			x, okx := parseFloat(rest[0])
			y, oky := parseFloat(rest[1])
			if !okx || !oky {
				return nil, usagef("%s: invalid numbers %q %q", name, rest[0], rest[1])
			}
			if name == "abs" && (x < 0 || x > 1 || y < 0 || y > 1) {
				return nil, usagef("abs: coordinates are fractions in 0..1, got %v %v", x, y)
			}
			a.x, a.y = x, y
			i += 2
			if name == "drag" {
				i += optButton(2)
			}
		case "press", "release", "click":
			i += optButton(0)
		case "hold":
			n := optButton(0)
			if len(rest) > n {
				if d, err := time.ParseDuration(rest[n]); err == nil {
					if d < 0 {
						return nil, usagef("hold: negative duration %s", rest[n])
					}
					a.duration = d
					n++
				}
			}
			i += n
		case "scroll":
			if err := need(1, "up, down, left or right"); err != nil {
				return nil, err
			}
			sign := 1
			switch rest[0] {
			case "up":
				sign = -1
			case "down":
			case "left":
				sign, a.axis = -1, 1
			case "right":
				a.axis = 1
			default:
				return nil, usagef("scroll: direction %q is not up, down, left or right", rest[0])
			}
			a.steps = sign
			i++
			if len(rest) > 1 {
				if n, err := strconv.Atoi(rest[1]); err == nil {
					if n <= 0 || n > 1000 {
						return nil, usagef("scroll: step count %d out of 1..1000", n)
					}
					a.steps = sign * n
					i++
				}
			}
		default:
			return nil, usagef("unknown action %q", name)
		}
		out = append(out, a)
	}
	return out, nil
}

func (o pointerOptions) validate() error {
	switch {
	case o.clickDuration < 0, o.holdDuration < 0, o.dragStepDelay < 0:
		return usagef("durations must not be negative")
	case o.dragSteps < 1 || o.dragSteps > 10000:
		return usagef("--drag-steps must be in 1..10000, got %d", o.dragSteps)
	}
	return nil
}

var pointerCmd = Command{
	Name: "pointer",
	Args: "[flags] <action> [args] [<action> [args]...]",
	Summary: "Drive the session pointer (zwlr_virtual_pointer_v1). Actions run in order:\n" +
		"  abs X Y                 move to fractions 0..1 of the layout (or --output)\n" +
		"  move DX DY              move by logical pixels\n" +
		"  press [BTN]             press a button        release [BTN]  release it\n" +
		"  click [BTN]             press and release after --click-duration\n" +
		"  hold [BTN] [DURATION]   press, wait (default --hold-duration), release\n" +
		"  drag DX DY [BTN]        press, move in --drag-steps, release\n" +
		"  scroll up|down|left|right [N]   N wheel steps (default 1)\n" +
		"BTN is left (default), right, middle, side, extra, forward, back, a BTN_* name\n" +
		"or an evdev code. Buttons still pressed at exit are released.",
	Setup: func(fs *flag.FlagSet) Runner {
		var o pointerOptions
		fs.StringVar(&o.output, "output", "", "map abs into this output (create_virtual_pointer_with_output)")
		fs.DurationVar(&o.clickDuration, "click-duration", 20*time.Millisecond, "time a click holds the button")
		fs.DurationVar(&o.holdDuration, "hold-duration", 500*time.Millisecond, "default time of hold")
		fs.IntVar(&o.dragSteps, "drag-steps", 10, "motion steps of a drag")
		fs.DurationVar(&o.dragStepDelay, "drag-step-delay", 10*time.Millisecond, "time between drag steps")
		return func(ctx context.Context, env Env, args []string) error {
			if err := o.validate(); err != nil {
				return err
			}
			actions, err := parsePointerActions(args)
			if err != nil {
				return err
			}
			return runPointer(ctx, o, actions)
		}
	},
}

// String names an action for errors.
func (a pointerAction) String() string { return a.kind }
