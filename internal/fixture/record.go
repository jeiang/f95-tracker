package fixture

import (
	"context"
	"fmt"
	"sort"

	"github.com/jeiang/f95-tracker/internal/f95"
)

// Record fetches thread id with the stored cookie and returns the scrubbed
// page. The stored cookie values are passed to the scrubber as secrets, so a
// page that echoes one back fails instead of being written.
func Record(ctx context.Context, c *f95.Client, creds *f95.CredStore, id string) ([]byte, error) {
	cred, ok, err := creds.Load(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("fixture: no stored cookie; save one in Settings first")
	}
	body, _, err := c.ThreadHTML(ctx, id)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(cred.Jar))
	for n := range cred.Jar {
		names = append(names, n)
	}
	sort.Strings(names)
	var secrets []string
	for _, n := range names {
		secrets = append(secrets, cred.Jar[n])
	}
	return Scrub(body, secrets...)
}
