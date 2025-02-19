package scp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/gliderlabs/ssh"
	"github.com/neurosnap/lists.sh/internal"
	"github.com/neurosnap/lists.sh/internal/db"
)

var (
	reTimestamp = regexp.MustCompile(`^T(\d{10}) 0 (\d{10}) 0$`)
	reNewFolder = regexp.MustCompile(`^D(\d{4}) 0 (.*)$`)
	reNewFile   = regexp.MustCompile(`^C(\d{4}) (\d+) (.*)$`)
)

type parseError struct {
	subject string
}

func (e parseError) Error() string {
	return fmt.Sprintf("failed to parse: %q", e.subject)
}

func copyFromClient(s ssh.Session, info Info, handler CopyFromClientHandler, user *db.User, dbpool db.DB) error {
	logger := internal.CreateLogger()
	// accepts the request
	_, _ = s.Write(NULL)

	writeErrors := []error{}

	var (
		path  = info.Path
		r     = bufio.NewReader(s)
		mtime int64
		atime int64
	)

	for {
		line, _, err := r.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("failed to read line: %w", err)
		}

		if matches := reTimestamp.FindAllStringSubmatch(string(line), 2); matches != nil {
			mtime, err = strconv.ParseInt(matches[0][1], 10, 64)
			if err != nil {
				return parseError{string(line)}
			}
			atime, err = strconv.ParseInt(matches[0][2], 10, 64)
			if err != nil {
				return parseError{string(line)}
			}

			// accepts the header
			_, _ = s.Write(NULL)
			continue
		}

		if matches := reNewFile.FindAllStringSubmatch(string(line), 3); matches != nil {
			if len(matches) != 1 || len(matches[0]) != 4 {
				return parseError{string(line)}
			}

			mode, err := strconv.ParseUint(matches[0][1], 8, 32)
			if err != nil {
				return parseError{string(line)}
			}

			size, err := strconv.ParseInt(matches[0][2], 10, 64)
			if err != nil {
				return parseError{string(line)}
			}
			name := matches[0][3]

			// accepts the header
			_, _ = s.Write(NULL)

			err = handler.Write(s, &FileEntry{
				Name:     name,
				Filepath: filepath.Join(path, name),
				Mode:     fs.FileMode(mode),
				Size:     size,
				Mtime:    mtime,
				Atime:    atime,
				Reader:   newLimitReader(r, int(size)),
			}, user, dbpool)

			if err != nil {
				writeErrors = append(writeErrors, err)
				logger.Infof("failed to write file: %s %q: %v", user.Name, name, err)
			}

			// read the trailing nil char
			_, _ = r.ReadByte() // TODO: check if it is indeed a NULL?

			mtime = 0
			atime = 0
			// says 'hey im done'
			_, _ = s.Write(NULL)
			continue
		}

		if matches := reNewFolder.FindAllStringSubmatch(string(line), 2); matches != nil {
			if len(matches) != 1 || len(matches[0]) != 3 {
				return parseError{string(line)}
			}

			name := matches[0][2]
			path = filepath.Join(path, name)
			// says 'hey im done'
			_, _ = s.Write(NULL)
			continue
		}

		if string(line) == "E" {
			path = filepath.Dir(path)

			// says 'hey im done'
			_, _ = s.Write(NULL)
			continue
		}

		return fmt.Errorf("unhandled input: %q", string(line))
	}

	for _, e := range writeErrors {
		_, _ = fmt.Fprintln(s.Stderr(), e)
	}

	_, _ = s.Write(NULL)
	return nil
}
