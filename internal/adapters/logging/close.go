package logging

import (
	"os"

	"bnema.dev/zerowrap"
	"golang.org/x/sys/unix"
)

// CloseFD closes fd once and logs a failure at Warn. It never retries: after
// close(2) fails the descriptor is not valid to close again. Use it where the
// error cannot be returned.
func CloseFD(log zerowrap.Logger, fd int, what string) {
	if err := unix.Close(fd); err != nil {
		log.Warn().Err(err).Int("fd", fd).Msg("close " + what)
	}
}

// CloseFile closes f once and logs a failure at Warn.
func CloseFile(log zerowrap.Logger, f *os.File, what string) {
	if err := f.Close(); err != nil {
		log.Warn().Err(err).Str("name", f.Name()).Msg("close " + what)
	}
}

// RemoveFile removes path and logs a failure at Warn. A file that is already
// gone is not a failure.
func RemoveFile(log zerowrap.Logger, path, what string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Warn().Err(err).Str("path", path).Msg("remove " + what)
	}
}
