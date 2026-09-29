package rt

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// file_info.go — Sky.Core.File metadata, links and confinement:
// `realPath`, `stat`, `lstat`, `readLink`, `chmod`, `resolveWithin`.
//
// Every kernel is a Task (returns a thunk). A failure maps to the Sky error
// kind that names it (fileError): a missing path is `NotFound`, a refused
// one `PermissionDenied`, anything else `Io`.
//
// Symlink semantics. `stat` follows symlinks (it describes the target) and
// `lstat` does not (a symlink is `Symlink`). `realPath` resolves every
// symlink and `..` in the path. On Windows, symlinks and junctions are
// reported as Go's `os` package reports them (a junction is not a Symlink),
// and `mode` holds only the bits Windows keeps (read-only = no write bits).

// fileKind* are Sky.Core.File.FileKind's constructor tags (File, Directory,
// Symlink, Other), the order the Sky source declares them.
const (
	fileKindFile      = 0
	fileKindDirectory = 1
	fileKindSymlink   = 2
	fileKindOther     = 3
)

// resolveMaxLinks bounds symlink expansion in resolvePath, as the kernel's
// own ELOOP limit does, so a symlink cycle is an error, not a hang.
const resolveMaxLinks = 40

// fileError maps a Go file-system error to a Sky Error: NotFound,
// PermissionDenied, or Io, its message prefixed with the operation.
func fileError(op string, err error) any {
	msg := op + ": " + err.Error()
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return makeError(5, "NotFound", msg)
	case errors.Is(err, fs.ErrPermission):
		return ErrPermissionDenied(msg)
	case errors.Is(err, fs.ErrInvalid):
		return ErrInvalidInput(msg)
	}
	return ErrIo(msg)
}

// fileInfoValue is the record the Sky side reads: `{ kind : Int, size : Int,
// modified : Int, mode : Int }` (Sky.Core.File turns `kind` into a FileKind).
// `modified` is Unix milliseconds, like `Time.now`; `mode` is the permission
// bits (0o644 = 420) with setuid / setgid / sticky in the Unix positions.
func fileInfoValue(fi fs.FileInfo) any {
	m := fi.Mode()
	kind := fileKindOther
	switch {
	case m&fs.ModeSymlink != 0:
		kind = fileKindSymlink
	case m.IsDir():
		kind = fileKindDirectory
	case m.IsRegular():
		kind = fileKindFile
	}
	perm := int(m.Perm())
	if m&fs.ModeSetuid != 0 {
		perm |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		perm |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		perm |= 0o1000
	}
	return map[string]any{
		"kind":     kind,
		"size":     int(fi.Size()),
		"modified": int(fi.ModTime().UnixMilli()),
		"mode":     perm,
	}
}

// File_stat : String -> Task Error RawInfo   (follows symlinks)
func File_stat(pathArg any) any {
	return func() any {
		fi, err := os.Stat(AsString(pathArg))
		if err != nil {
			return Err[any, any](fileError("File.stat", err))
		}
		return Ok[any, any](fileInfoValue(fi))
	}
}

// File_lstat : String -> Task Error RawInfo   (a symlink is described itself)
func File_lstat(pathArg any) any {
	return func() any {
		fi, err := os.Lstat(AsString(pathArg))
		if err != nil {
			return Err[any, any](fileError("File.lstat", err))
		}
		return Ok[any, any](fileInfoValue(fi))
	}
}

// File_readLink : String -> Task Error String   (the link's target, as stored)
func File_readLink(pathArg any) any {
	return func() any {
		target, err := os.Readlink(AsString(pathArg))
		if err != nil {
			return Err[any, any](fileError("File.readLink", err))
		}
		return Ok[any, any](target)
	}
}

// File_chmod : Int -> String -> Task Error ()
func File_chmod(modeArg, pathArg any) any {
	return func() any {
		if r := ssrSuppressedWrite("file.chmod"); r != nil {
			return r
		}
		mode := AsInt(modeArg)
		if mode < 0 || mode > 0o7777 {
			return Err[any, any](ErrInvalidInput(fmt.Sprintf(
				"File.chmod: mode %#o is outside 0 .. 0o7777", mode)))
		}
		fm := fs.FileMode(mode & 0o777)
		if mode&0o4000 != 0 {
			fm |= fs.ModeSetuid
		}
		if mode&0o2000 != 0 {
			fm |= fs.ModeSetgid
		}
		if mode&0o1000 != 0 {
			fm |= fs.ModeSticky
		}
		if err := os.Chmod(AsString(pathArg), fm); err != nil {
			return Err[any, any](fileError("File.chmod", err))
		}
		return Ok[any, any](struct{}{})
	}
}

// File_realPath : String -> Task Error String
//
// The absolute path with every symlink and `.` / `..` resolved. The path
// must exist (`NotFound` otherwise).
func File_realPath(pathArg any) any {
	return func() any {
		p := AsString(pathArg)
		abs, err := filepath.Abs(p)
		if err != nil {
			return Err[any, any](fileError("File.realPath", err))
		}
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return Err[any, any](fileError("File.realPath", err))
		}
		return Ok[any, any](real)
	}
}

// File_resolveWithin : String -> String -> Task Error String
//
// `resolveWithin root path` resolves `path` (relative paths are taken
// relative to `root`) with every symlink and `..` followed, and returns it
// only when the result is `root` itself or inside it; otherwise
// `Err PermissionDenied`. The final components may not exist yet (a file
// about to be created); every component that exists is resolved. `root` must
// exist. This is a check at one moment: a symlink created inside the root
// after the call can still point outside it, so use the returned path
// promptly and do not let untrusted parties write links inside the root.
func File_resolveWithin(rootArg, pathArg any) any {
	return func() any {
		root, err := filepath.Abs(AsString(rootArg))
		if err != nil {
			return Err[any, any](fileError("File.resolveWithin", err))
		}
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return Err[any, any](fileError("File.resolveWithin: root", err))
		}
		p := AsString(pathArg)
		if p == "" {
			return Err[any, any](ErrInvalidInput("File.resolveWithin: empty path"))
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(realRoot, p)
		}
		resolved, err := resolvePath(p)
		if err != nil {
			return Err[any, any](fileError("File.resolveWithin", err))
		}
		if !pathWithin(realRoot, resolved) {
			return Err[any, any](ErrPermissionDenied(fmt.Sprintf(
				"File.resolveWithin: %s resolves to %s, outside the root %s",
				AsString(pathArg), resolved, realRoot)))
		}
		return Ok[any, any](resolved)
	}
}

// pathWithin reports whether `p` is `root` or below it. Both are absolute and
// resolved.
func pathWithin(root, p string) bool {
	if p == root {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(p, prefix)
}

// resolvePath resolves an absolute path one component at a time, the way the
// kernel walks it: a symlink is replaced by its target (relative targets
// against the link's directory) and re-walked, `..` steps to the parent of
// what has been resolved so far (so `link/..` is the parent of the link's
// TARGET, not of the link). A component that does not exist ends the
// resolution; the rest of the path is appended lexically.
func resolvePath(p string) (string, error) {
	links := 0
	vol := filepath.VolumeName(p)
	rest := strings.Split(filepath.ToSlash(p[len(vol):]), "/")
	cur := vol + string(filepath.Separator)
	missing := false
	for len(rest) > 0 {
		comp := rest[0]
		rest = rest[1:]
		switch comp {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, comp)
		if missing {
			cur = next
			continue
		}
		fi, err := os.Lstat(next)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				missing = true
				cur = next
				continue
			}
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			cur = next
			continue
		}
		links++
		if links > resolveMaxLinks {
			return "", fmt.Errorf("%s: too many levels of symbolic links", p)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		tvol := filepath.VolumeName(target)
		tparts := strings.Split(filepath.ToSlash(target[len(tvol):]), "/")
		if filepath.IsAbs(target) {
			cur = tvol + string(filepath.Separator)
		}
		rest = append(tparts, rest...)
	}
	return filepath.Clean(cur), nil
}
