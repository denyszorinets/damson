package devnodes

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/denyszorinets/damson/internal/inventory"
	"github.com/denyszorinets/damson/internal/secrets"
	"github.com/denyszorinets/damson/internal/store"
)

// SecretWriter is what the seeder needs from the secret store.
type SecretWriter interface {
	Meta(ctx context.Context, scope, name string) (store.SecretMeta, error)
	Get(ctx context.Context, scope, name string) (map[string]string, error)
	Put(ctx context.Context, scope, name, typ string, fields map[string]string, ifVersion *int64) (store.SecretMeta, bool, error)
}

// Report is what seeding did. Warnings name nodes and refs only.
type Report struct {
	Nodes            inventory.Diff
	SecretsWritten   []string // refs that got a new version (or were created)
	SecretsUnchanged []string
	Warnings         []string
}

// maxSecretAttempts bounds re-reads when a concurrent seeder wrote first.
const maxSecretAttempts = 3

// Seed writes the secrets and nodes of f into the stores. It is idempotent:
// unchanged nodes and secrets are not written, so versions stay. Nodes of the
// devfile source that are no longer in the file are removed; secrets are never
// removed (they have no owner), delete them through the API. Secret values are
// read from their files now and cleared when done.
func Seed(ctx context.Context, nodes store.NodeStore, sec SecretWriter, f *File, now time.Time) (Report, error) {
	var rep Report
	for _, def := range f.secrets {
		wrote, err := seedSecret(ctx, sec, def)
		if err != nil {
			return rep, err
		}
		if wrote {
			rep.SecretsWritten = append(rep.SecretsWritten, def.ref.Base())
		} else {
			rep.SecretsUnchanged = append(rep.SecretsUnchanged, def.ref.Base())
		}
	}
	diff, err := inventory.Sync(ctx, nodes, Source, f.Desired(), true, false, now)
	rep.Nodes = diff
	if err != nil {
		if errors.Is(err, inventory.ErrSyncConflict) {
			return rep, fmt.Errorf("%w: %s", err, describeConflicts(diff.Conflicts))
		}
		return rep, fmt.Errorf("seeding nodes: %w", err)
	}
	rep.Warnings = f.warnings()
	return rep, nil
}

func describeConflicts(cs []inventory.Conflict) string {
	s := ""
	for i, c := range cs {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprintf("node %q is owned by source %q", c.Name, c.OwnedBy)
	}
	return s
}

// seedSecret makes the stored secret equal to the file's. It returns whether it
// wrote a version.
func seedSecret(ctx context.Context, sec SecretWriter, def secretDef) (bool, error) {
	fields, err := def.read()
	if err != nil {
		return false, err
	}
	defer clear(fields)
	scope, name, ref := def.ref.Scope, def.ref.Name, def.ref.Base()
	for range maxSecretAttempts {
		meta, err := sec.Meta(ctx, scope, name)
		var ifVersion int64
		switch {
		case errors.Is(err, secrets.ErrNotFound):
			// ifVersion stays 0: create only
		case err != nil:
			return false, fmt.Errorf("secret %s: %w", ref, err)
		default:
			if meta.Type != def.typ {
				return false, fmt.Errorf("secret %s exists with type %q but the dev file says %q", ref, meta.Type, def.typ)
			}
			cur, err := sec.Get(ctx, scope, name)
			if errors.Is(err, secrets.ErrNotFound) {
				continue // deleted between the two reads: look again
			}
			if err != nil {
				return false, fmt.Errorf("secret %s: %w", ref, err)
			}
			same := sameFields(cur, fields)
			clear(cur)
			if same {
				return false, nil
			}
			ifVersion = meta.CurrentVersion
		}
		_, _, err = sec.Put(ctx, scope, name, def.typ, fields, &ifVersion)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, secrets.ErrVersionConflict) {
			return false, fmt.Errorf("secret %s: %w", ref, err)
		}
		// Another seeder won: read again and compare.
	}
	return false, fmt.Errorf("secret %s: changed concurrently, giving up", ref)
}

// read returns the fields of the secret with the file values loaded.
func (d secretDef) read() (map[string]string, error) {
	out := maps.Clone(d.literals)
	for field, path := range d.files {
		b, err := readSecretFile(path)
		if err != nil {
			clear(out)
			return nil, fmt.Errorf("secret %s field %q: %w", d.ref.Base(), field, err)
		}
		out[field] = string(b)
		clear(b)
	}
	return out, nil
}

// sameFields compares two field sets without leaking where they differ.
func sameFields(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	equal := true
	for k, av := range a {
		bv, ok := b[k]
		if !ok || subtle.ConstantTimeCompare([]byte(av), []byte(bv)) != 1 {
			equal = false
		}
	}
	return equal
}

// warnings names the nodes that dispatch will refuse until host key handling
// exists and the _inventory secrets no node uses.
func (f *File) warnings() []string {
	var w []string
	used := map[string]bool{}
	for _, n := range f.nodes {
		used[n.Credential] = true
		if n.Credential != "" && !strings.HasPrefix(n.Credential, "secret://"+secrets.InventoryScope+"/") {
			w = append(w, fmt.Sprintf("node %q: credential %s is not in the _inventory scope: it is only usable by jobs of that project", n.Name, n.Credential))
		}
		if n.HostKeyPolicy == inventory.PolicyInsecure || (n.HostKeyPolicy == inventory.PolicyTOFU && n.HostKey == "") {
			w = append(w, fmt.Sprintf("node %q has host key policy %s without a pinned key: dispatch refuses it until host key handling lands (#118)", n.Name, n.HostKeyPolicy))
		}
	}
	for _, d := range f.secrets {
		if d.ref.Scope == secrets.InventoryScope && !used[d.ref.Base()] {
			w = append(w, fmt.Sprintf("secret %s is in the _inventory scope but no node uses it as its credential: it cannot be used", d.ref.Base()))
		}
	}
	sort.Strings(w)
	return w
}
