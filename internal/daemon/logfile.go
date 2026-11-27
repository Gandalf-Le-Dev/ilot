package daemon

import "io"

// lossyWriter drops what its target refuses. The reader of a pipe must go on, and a later write can succeed.
type lossyWriter struct {
	target io.Writer
	// Called at the first write of each row of writes that fail.
	onLoss   func(error)
	isLosing bool
}

func (lossy *lossyWriter) Write(data []byte) (int, error) {
	_, err := lossy.target.Write(data)
	if err != nil && !lossy.isLosing {
		lossy.onLoss(err)
	}
	lossy.isLosing = err != nil
	return len(data), nil
}
