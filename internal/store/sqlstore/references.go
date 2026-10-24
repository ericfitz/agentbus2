package sqlstore

import (
	"context"
	"fmt"

	"github.com/ericfitz/agentbus2/internal/store"
)

// The users and admins reference lists are new in v2 (federated identity spec
// 6.2); no v1 source.

type refRow struct {
	List      string `db:"ref_list"`
	Kind      string `db:"ref_kind"`
	Issuer    string `db:"issuer"`
	Value     string `db:"value"`
	CreatedAt int64  `db:"created_at"`
}

// checkReference validates r for a write.
func checkReference(r store.Reference) error {
	if !store.ValidRefList(r.List) {
		return fmt.Errorf("%w: reference list %q (want users or admins)", store.ErrInvalid, r.List)
	}
	if !store.ValidRefKind(r.Kind) {
		return fmt.Errorf("%w: reference kind %q (want email, group, subject or identity)", store.ErrInvalid, r.Kind)
	}
	switch {
	case r.Kind == store.RefIdentity && r.Issuer != "":
		return fmt.Errorf("%w: an identity reference has no issuer", store.ErrInvalid)
	case r.Kind != store.RefIdentity && r.Issuer == "":
		return fmt.Errorf("%w: a %s reference needs an issuer", store.ErrInvalid, r.Kind)
	case r.Value == "":
		return fmt.Errorf("%w: reference value must not be empty", store.ErrInvalid)
	}
	return checkText("reference", "issuer", r.Issuer, "value", r.Value)
}

func (t *tx) AddReference(ctx context.Context, r store.Reference) (bool, error) {
	if err := t.writable(); err != nil {
		return false, err
	}
	if err := checkReference(r); err != nil {
		return false, err
	}
	res, err := t.execW(ctx, "INSERT INTO access_refs (ref_list, ref_kind, issuer, value, created_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING",
		string(r.List), string(r.Kind), r.Issuer, r.Value, r.CreatedAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (t *tx) RemoveReference(ctx context.Context, r store.Reference) (bool, error) {
	if err := t.writable(); err != nil {
		return false, err
	}
	if err := checkReference(r); err != nil {
		return false, err
	}
	res, err := t.execW(ctx, "DELETE FROM access_refs WHERE ref_list = ? AND ref_kind = ? AND issuer = ? AND value = ?",
		string(r.List), string(r.Kind), r.Issuer, r.Value)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (t *tx) ListReferences(ctx context.Context, list store.RefList) ([]store.Reference, error) {
	if !store.ValidRefList(list) {
		return nil, fmt.Errorf("%w: reference list %q (want users or admins)", store.ErrInvalid, list)
	}
	var rows []refRow
	if err := t.sel(ctx, &rows, "SELECT ref_list, ref_kind, issuer, value, created_at FROM access_refs WHERE ref_list = ?", string(list)); err != nil {
		return nil, err
	}
	// NUL cannot occur in a stored text, so it separates the sort key safely.
	sortByBytes(rows, func(r refRow) string { return r.Kind + "\x00" + r.Issuer + "\x00" + r.Value })
	out := make([]store.Reference, len(rows))
	for i, r := range rows {
		out[i] = store.Reference{List: store.RefList(r.List), Kind: store.RefKind(r.Kind), Issuer: r.Issuer, Value: r.Value, CreatedAt: r.CreatedAt}
	}
	return out, nil
}
