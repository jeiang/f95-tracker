package worker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// SanitizeName makes a game name or version safe as one directory name
// (R-DL-9): '/' and NUL become '_', leading dots become '_', the rest is kept.
func SanitizeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '/' || r == 0 {
			return '_'
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	n := len(s) - len(strings.TrimLeft(s, "."))
	s = strings.Repeat("_", n) + s[n:]
	if s == "" {
		return "_"
	}
	return s
}

var (
	rarFirstPart = regexp.MustCompile(`(?i)\.part0*1\.rar$`)
	rarLaterPart = regexp.MustCompile(`(?i)\.part0*[2-9]\d*\.rar$|\.part0*[1-9]\d+\.rar$`)
	rarPlain     = regexp.MustCompile(`(?i)\.rar$`)
)

type archiveKind int

const (
	notArchive archiveKind = iota
	kindSevenZip
	kindRar
)

// archiveEntry classifies a file: it returns the extractor kind when the
// file is the first volume of an archive set (or a single archive).
// Later volumes (.part2.rar, .002, .r00) are not entries; the extractor
// finds them next to the first one.
func archiveEntry(name string) archiveKind {
	l := strings.ToLower(name)
	switch {
	case strings.HasSuffix(l, ".zip"), strings.HasSuffix(l, ".7z"), strings.HasSuffix(l, ".001"):
		return kindSevenZip
	case rarFirstPart.MatchString(l):
		return kindRar
	case rarLaterPart.MatchString(l):
		return notArchive
	case rarPlain.MatchString(l):
		return kindRar
	}
	return notArchive
}

func listFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	return files, err
}

// install puts the downloaded files into dest: archives are extracted,
// otherwise the files are moved as-is.
func install(ctx context.Context, files []string, dest string) error {
	if len(files) == 0 {
		return errors.New("download produced no files")
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	extracted := false
	for _, f := range files {
		k := archiveEntry(filepath.Base(f))
		if k == notArchive {
			continue
		}
		extracted = true
		if err := extract(ctx, k, f, dest); err != nil {
			return err
		}
	}
	if extracted {
		return nil
	}
	for _, f := range files {
		if err := moveFile(f, filepath.Join(dest, filepath.Base(f))); err != nil {
			return err
		}
	}
	return nil
}

func extract(ctx context.Context, k archiveKind, archive, dest string) error {
	var cmd *exec.Cmd
	if k == kindRar {
		cmd = exec.CommandContext(ctx, "unar", "-quiet", "-force-overwrite", "-no-directory", "-output-directory", dest, archive)
	} else {
		cmd = exec.CommandContext(ctx, "7zz", "x", "-y", "-aoa", "-bd", "-o"+dest, "--", archive)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("extract %s: %w: %s", filepath.Base(archive), err, tail(out))
	}
	return nil
}

func moveFile(from, to string) error {
	if err := os.Rename(from, to); err == nil {
		return nil
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := out.ReadFrom(in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(from)
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 200 {
		s = s[len(s)-200:]
	}
	return s
}
