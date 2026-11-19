package http

import (
	"bytes"
	"net/http"
)

// capture records the handler's response so it can be persisted and committed
// BEFORE anything reaches the client.
type capture struct {
	header http.Header
	code   int
	body   bytes.Buffer
	// class and record are the replay class of the problem the handler answered
	// (MarkReplay, set by writeError); replayUnset for a success.
	class  replayClass
	record bool
}

func (c *capture) Header() http.Header { return c.header }

func (c *capture) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
	}
}

func (c *capture) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	return c.body.Write(b)
}

// MarkReplay records the class of the problem about to be written: the
// middleware decides from it whether the answer is stored, committed with the
// handler's writes (an OUTCOME) or released (a RETRYABLE refusal).
func (c *capture) MarkReplay(class replayClass, record bool) { c.class, c.record = class, record }

// settlement is what the middleware does with this answer.
func (c *capture) settlement() settlement { return settlementOf(c.code, c.class, c.record) }

// flush copies the captured response onto the real client connection.
func flush(w http.ResponseWriter, rec *capture) {
	for name, vs := range rec.header {
		for _, v := range vs {
			w.Header().Add(name, v)
		}
	}
	w.WriteHeader(rec.code)
	_, _ = w.Write(rec.body.Bytes())
}
