package workspace

import (
	"strings"

	"github.com/bnema/neferwl/internal/domain"
)

// MarkOp is the operation of a mark action.
type MarkOp uint8

// Mark operations, one per mark action prefix.
const (
	MarkSet MarkOp = iota + 1
	MarkToggle
	MarkUnmark
	MarkFocus
	MarkSwap
	MarkMoveTo
)

// Mark action prefixes: "mark term", "focus-mark term foot".
const (
	markPrefix       = "mark "
	toggleMarkPrefix = "toggle-mark "
	unmarkPrefix     = "unmark "
	focusMarkPrefix  = "focus-mark "
	swapMarkPrefix   = "swap-mark "
	moveToMarkPrefix = "move-to-mark "
)

var markPrefixes = []struct {
	prefix string
	op     MarkOp
}{
	{markPrefix, MarkSet},
	{toggleMarkPrefix, MarkToggle},
	{unmarkPrefix, MarkUnmark},
	{focusMarkPrefix, MarkFocus},
	{swapMarkPrefix, MarkSwap},
	{moveToMarkPrefix, MarkMoveTo},
}

// MarkArg parses a mark action. A bare "unmark" gives MarkUnmark with name
// "". cmd is set only for focus-mark with a command after the name. ok is
// false on a missing or invalid name, or extra fields on other ops.
func MarkArg(a Action) (op MarkOp, name string, cmd []string, ok bool) {
	if a == Action(strings.TrimSpace(unmarkPrefix)) {
		return MarkUnmark, "", nil, true
	}
	for _, p := range markPrefixes {
		rest, found := strings.CutPrefix(string(a), p.prefix)
		if !found {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 || !domain.ValidMarkName(fields[0]) {
			return 0, "", nil, false
		}
		if len(fields) > 1 {
			if p.op != MarkFocus {
				return 0, "", nil, false
			}
			cmd = fields[1:]
		}
		return p.op, fields[0], cmd, true
	}
	return 0, "", nil, false
}
