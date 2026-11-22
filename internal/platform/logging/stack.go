package logging

import (
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const maxFrames = 32

// Stack returns the caller's stack as "function file:line" frames, skipping
// skip frames above the caller. Runtime and net/http frames are left out;
// they are the same for every request.
func Stack(skip int) []string {
	pcs := make([]uintptr, maxFrames+16)
	n := runtime.Callers(skip+2, pcs)
	frames := runtime.CallersFrames(pcs[:n])

	var out []string
	for len(out) < maxFrames {
		f, more := frames.Next()
		if !strings.HasPrefix(f.Function, "runtime.") && !strings.HasPrefix(f.Function, "net/http.") {
			out = append(out, shortFunc(f.Function)+" "+filepath.Base(f.File)+":"+strconv.Itoa(f.Line))
		}
		if !more {
			break
		}
	}
	return out
}

// shortFunc drops the import path: "github.com/x/y/internal/httpapi.(*h).f"
// becomes "httpapi.(*h).f".
func shortFunc(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}
