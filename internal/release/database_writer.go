package release

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

var errDatabaseWriter = errors.New("release database writer did not complete")

type databaseWriterProcess interface {
	identity() (databaseProcessIdentity, error)
	wait() error
	terminate() error
	close()
}

// runDatabaseWriter is for the verified, single-process pg_restore invocation.
// The caller supplies an immutable regular-file spool and a bounded context.
// Only a pipe is inherited as stdin; the archive handle stays in the parent.
// No raw client diagnostics, arguments or environment are returned on failure.
func runDatabaseWriter(ctx context.Context, executable string, args, environment []string, directory string, input *os.File) error {
	return runPreparedDatabaseWriter(ctx, executable, args, environment, directory, input, nil)
}

// prepare runs synchronously before any archive byte can reach the child. It
// must durably record the identity before returning nil, and honor ctx. Failure
// kills and reaps the child without advancing input. A record alone does not
// prove whether a transaction committed; recovery must also inspect SQL state.
func runPreparedDatabaseWriter(ctx context.Context, executable string, args, environment []string, directory string,
	input *os.File, prepare func(context.Context, databaseProcessIdentity) error) error {
	if _, ok := ctx.Deadline(); !ok || ctx.Err() != nil || input == nil || environment == nil ||
		!filepath.IsAbs(executable) || !filepath.IsAbs(directory) {
		return errDatabaseWriter
	}
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errDatabaseWriter
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return errDatabaseWriter
	}
	defer reader.Close()
	defer writer.Close()
	output, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return errDatabaseWriter
	}
	defer output.Close()

	// Linux parent-death notification follows the creating OS thread. Keep it
	// alive until the child has been reaped, even if the Go scheduler moves work.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	process, err := startDatabaseWriter(executable, args, environment, directory, reader, output)
	if err != nil {
		return errDatabaseWriter
	}
	defer process.close()
	reader.Close()
	if prepare != nil {
		identity, observeErr := process.identity()
		if observeErr == nil && ctx.Err() == nil {
			observeErr = prepare(ctx, identity)
		}
		if observeErr != nil || ctx.Err() != nil {
			_ = process.terminate()
			_ = process.wait()
			return errDatabaseWriter
		}
	}
	copied := make(chan error, 1)
	go func() {
		_, err := io.Copy(writer, input)
		writer.Close()
		copied <- err
	}()
	waited := make(chan error, 1)
	go func() { waited <- process.wait() }()
	select {
	case err = <-waited:
	case <-ctx.Done():
		_ = process.terminate()
		err = <-waited
	}
	// Closing our pipe also releases a blocked copy after early client exit.
	writer.Close()
	copyErr := <-copied
	if err != nil || copyErr != nil || ctx.Err() != nil {
		return errDatabaseWriter
	}
	return nil
}
