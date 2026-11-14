package domain

import "strings"

// ValidOutputName reports whether name is a non-empty output identifier made
// of ASCII letters, digits, dots, underscores or hyphens, excluding the names "." and "..".
func ValidOutputName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		strings.Trim(name, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._-") == ""
}
