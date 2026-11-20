package download

// Serialize the same rendition across URLs and destination directories.
// Each waiter can still cancel independently.
func (j *job) lockMedia(m media) (func(), error) {
	key := j.key(m)
	for {
		if err := j.ctx.Err(); err != nil {
			return nil, err
		}
		j.mu.Lock()
		pending := j.pending[key]
		if pending == nil {
			pending = make(chan struct{})
			j.pending[key] = pending
			j.mu.Unlock()
			return func() {
				j.mu.Lock()
				delete(j.pending, key)
				close(pending)
				j.mu.Unlock()
			}, nil
		}
		j.mu.Unlock()
		select {
		case <-j.ctx.Done():
			return nil, j.ctx.Err()
		case <-pending:
		}
	}
}
