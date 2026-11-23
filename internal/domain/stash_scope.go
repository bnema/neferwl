package domain

// StashScope is who owns a stash (stash.scope): each workspace, each
// output, shared by its workspaces, or the whole session, shown on one
// output at a time.
type StashScope uint8

const (
	StashScopeWorkspace StashScope = iota
	StashScopeOutput
	StashScopeGlobal
)

var stashScopeNames = [...]string{
	StashScopeWorkspace: "workspace", StashScopeOutput: "output", StashScopeGlobal: "global",
}

// String returns the config spelling of s.
func (s StashScope) String() string {
	if int(s) < len(stashScopeNames) {
		return stashScopeNames[s]
	}
	return "unknown"
}

// ParseStashScope reads a stash.scope value.
func ParseStashScope(v string) (StashScope, bool) {
	for i, n := range stashScopeNames {
		if n == v {
			return StashScope(i), true
		}
	}
	return 0, false
}
