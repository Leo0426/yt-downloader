package instance

import "os"

// Hold keeps one writer per application data directory. The OS releases the lock after a crash.
func Hold(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	ok, err := acquire(f)
	if !ok || err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
