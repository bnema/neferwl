package peerexe

import (
	"io/fs"
	"strconv"
	"strings"

	"github.com/bnema/neferwl/internal/domain"
)

// Processes reads process identities from a proc-root filesystem. Its cut set
// is computed once at construction, before any window maps.
type Processes struct {
	fs  fs.FS
	cut map[int]bool
}

func NewProcesses(root fs.FS, compositor int) *Processes {
	p := &Processes{fs: root, cut: make(map[int]bool)}
	for compositor > 1 && !p.cut[compositor] {
		p.cut[compositor] = true
		_, parent, ok := p.read(compositor)
		if !ok {
			break
		}
		compositor = parent
	}
	return p
}

// Chain returns own process at element 0, then nearest-first ancestors.
// Chains exclude
// init and the compositor's ancestry, and contain at most 32 nodes.
func (p *Processes) Chain(pid int) []domain.ProcNode {
	var chain []domain.ProcNode
	seen := make(map[int]bool)
	for len(chain) < 32 && pid > 1 && !p.cut[pid] && !seen[pid] {
		seen[pid] = true
		node, parent, ok := p.read(pid)
		if !ok {
			break
		}
		chain = append(chain, node)
		pid = parent
	}
	return chain
}

func (p *Processes) read(pid int) (domain.ProcNode, int, bool) {
	b, err := fs.ReadFile(p.fs, strconv.Itoa(pid)+"/stat")
	if err != nil {
		return domain.ProcNode{}, 0, false
	}
	return parseStat(pid, string(b))
}

// comm may contain spaces and parentheses: fields begin after the LAST ')'.
func parseStat(pid int, stat string) (domain.ProcNode, int, bool) {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return domain.ProcNode{}, 0, false
	}
	f := strings.Fields(stat[end+1:]) // starts at field 3 (state)
	if len(f) < 20 || len(f[0]) != 1 {
		return domain.ProcNode{}, 0, false
	}
	parent, e1 := strconv.Atoi(f[1])
	session, e2 := strconv.Atoi(f[3])
	start, e3 := strconv.ParseUint(f[19], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || parent < 0 || session < 0 {
		return domain.ProcNode{}, 0, false
	}
	return domain.ProcNode{Key: domain.ProcKey{PID: pid, Start: start}, SessionLeader: session == pid}, parent, true
}
