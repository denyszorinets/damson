package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/denyszorinets/damson/internal/secrets"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

// kekGenerate prints a new KEK (base64, 32 random bytes) to stdout and its
// kek_id to stderr. It needs no database and stores nothing.
func kekGenerate(stdout, stderr io.Writer) error {
	k := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		return fmt.Errorf("generating KEK: %w", err)
	}
	defer clear(k)
	fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(k))
	fmt.Fprintf(stderr, "kek_id %s\nKeep this key safe: losing it loses every secret. Set it as DAMSON_KEK or in the file named by secrets.kek_file.\n", secrets.KeyID(k))
	return nil
}

// adminTokenCreate makes a new admin bootstrap token and replaces any existing
// one, which stops working at once. Only the hash is stored.
func adminTokenCreate(ctx context.Context, st store.Store, c adminCmd, _ time.Time) error {
	plain, hash, err := tokens.GenerateWithPrefix(rand.Reader, tokens.AdminPrefix)
	if err != nil {
		return err
	}
	cur, err := st.GetSetting(ctx, tokens.AdminSettingKey)
	switch {
	case errors.Is(err, store.ErrNotFound):
		_, err = st.CreateSetting(ctx, tokens.AdminSettingKey, hash)
	case err == nil:
		_, err = st.UpdateSetting(ctx, tokens.AdminSettingKey, hash, cur.Version)
	}
	if err != nil {
		return fmt.Errorf("storing admin token: %w", err)
	}
	if c.json {
		err = writeJSON(c.stdout, map[string]string{"token": plain})
	} else {
		_, err = fmt.Fprintln(c.stdout, plain)
	}
	if err != nil {
		return fmt.Errorf("writing token: %w", err)
	}
	fmt.Fprintln(c.stderr, "Store this token now: it is shown only once. Any earlier admin token no longer works.")
	return nil
}

// adminTokenRevoke disables the admin token. The secrets and inventory write
// endpoints then answer 401 until a new token is created.
func adminTokenRevoke(ctx context.Context, st store.Store, c adminCmd, _ time.Time) error {
	cur, err := st.GetSetting(ctx, tokens.AdminSettingKey)
	if errors.Is(err, store.ErrNotFound) || (err == nil && cur.Value == "") {
		return errors.New("there is no admin token")
	}
	if err == nil {
		_, err = st.UpdateSetting(ctx, tokens.AdminSettingKey, "", cur.Version)
	}
	if err != nil {
		return fmt.Errorf("revoking admin token: %w", err)
	}
	if c.json {
		return writeJSON(c.stdout, map[string]string{"status": "revoked"})
	}
	fmt.Fprintln(c.stdout, "revoked the admin token")
	return nil
}
