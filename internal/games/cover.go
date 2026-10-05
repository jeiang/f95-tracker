package games

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jeiang/f95-tracker/internal/db/sqlcgen"
	"github.com/jeiang/f95-tracker/internal/domain"
)

const maxCoverBytes = 10 << 20

// ErrCoverRejected marks a response that is not an acceptable cover image.
var ErrCoverRejected = errors.New("cover rejected")

var coverExt = map[string]string{
	"image/jpeg": "jpg", "image/png": "png", "image/gif": "gif", "image/webp": "webp", "image/avif": "avif",
}

// FetchCover downloads the image at url into <state>/covers/<gameID>.<ext> and
// updates the cover columns. Any failure leaves the previous cover untouched.
func (s *Service) FetchCover(ctx context.Context, gameID int64, url string) error {
	g, err := s.store.Queries().GetGame(ctx, gameID)
	if err != nil {
		return mapNoRows(err, "game", gameID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return invalid("cover url: %v", err)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("cover fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: http %d", ErrCoverRejected, resp.StatusCode)
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if !strings.HasPrefix(mt, "image/") {
		return fmt.Errorf("%w: content type %q", ErrCoverRejected, mt)
	}
	if resp.ContentLength > maxCoverBytes {
		return fmt.Errorf("%w: larger than %d bytes", ErrCoverRejected, maxCoverBytes)
	}
	ext, ok := coverExt[mt]
	if !ok {
		ext = strings.NewReplacer("+", ".", "x-", "").Replace(strings.TrimPrefix(mt, "image/"))
		if ext == "" || strings.ContainsAny(ext, "/\\") {
			return fmt.Errorf("%w: content type %q", ErrCoverRejected, mt)
		}
	}
	if err := os.MkdirAll(s.covers, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.covers, ".cover-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, maxCoverBytes+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("cover fetch: %w", err)
	}
	if n > maxCoverBytes {
		return fmt.Errorf("%w: larger than %d bytes", ErrCoverRejected, maxCoverBytes)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	rel := filepath.Join("covers", strconv.FormatInt(gameID, 10)+"."+ext)
	if err := os.Rename(tmp.Name(), filepath.Join(s.state, rel)); err != nil {
		return err
	}
	now := s.now()
	err = s.store.WithTx(ctx, func(q *sqlcgen.Queries) error {
		return q.UpdateGameCover(ctx, sqlcgen.UpdateGameCoverParams{
			CoverPath: nullStr(rel), CoverSourceUrl: nullStr(url), CoverFetchedAt: nullStr(now), UpdatedAt: now, ID: gameID,
		})
	})
	if err == nil && g.CoverPath.Valid && g.CoverPath.String != rel {
		os.Remove(filepath.Join(s.state, g.CoverPath.String))
	}
	return err
}

// CoverPath returns the absolute path of the Game's cached cover, or ErrNotFound.
func (s *Service) CoverPath(ctx context.Context, gameID int64) (string, error) {
	g, err := s.store.Queries().GetGame(ctx, gameID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !g.CoverPath.Valid) {
		return "", notFound("cover of game", gameID)
	}
	if err != nil {
		return "", err
	}
	p := filepath.Join(s.state, g.CoverPath.String)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("%w: cover file of game %d", domain.ErrNotFound, gameID)
	}
	return p, nil
}
