package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"jagex_account_creator/internal/config"
	"jagex_account_creator/internal/store"
)

// runExport writes the accounts of the store to a new file at out as JSONL.
func runExport(ctx context.Context, logger *slog.Logger, cfg *config.Config, out string) error {
	if out == "" {
		return errors.New("export mode needs -out")
	}
	accounts, err := openStore(ctx, logger, cfg)
	if err != nil {
		return err
	}
	exportAll := func(ctx context.Context, w io.Writer) error {
		return accounts.ExportJSONL(ctx, w, store.AccountFilter{})
	}
	err = errors.Join(exportFile(ctx, out, exportAll), accounts.Close())
	if err != nil {
		return err
	}
	logger.Info("exported accounts", "path", out)
	return nil
}

// exportFile creates the file out with mode 0600 and writes it with export. It
// removes the file on an error, so no partial export stays.
func exportFile(ctx context.Context, out string, export func(context.Context, io.Writer) error) error {
	// O_EXCL keeps an existing file. A shell redirect would give the file mode 0644 on Unix.
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("creating export file: %w", err)
	}
	// The errors of Close and Remove name their operation and the path.
	err = errors.Join(export(ctx, f), f.Close())
	if err != nil {
		return errors.Join(err, os.Remove(out))
	}
	return nil
}
