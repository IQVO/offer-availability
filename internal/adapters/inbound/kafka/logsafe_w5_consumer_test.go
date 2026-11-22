package kafka

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

// TestSellerConsumerLogsNoEventDerivedLineBreaks feeds the seller consumer a
// payload that is not a CloudEvent and a CloudEvent of a type it does not
// consume whose id, type, subject and data carry CR/LF: no log attribute may
// carry the line break.
func TestSellerConsumerLogsNoEventDerivedLineBreaks(t *testing.T) {
	rec := newCRLFRecorder()
	c := &SellerConsumer{Logger: slog.New(rec)}
	ctx := context.Background()
	_ = c.HandleMessage(ctx, []byte("not a CloudEvent"+crlfForged))
	_ = c.HandleMessage(ctx, crlfCloudEvent(t, "id"+crlfForged, "logsafe.Evil"+crlfForged))
	rec.assertNoLineBreaks(t, 2)
}

// crlfCloudEvent is a structured-mode CloudEvent whose string attributes are
// the given, hostile values.
func crlfCloudEvent(t *testing.T, id, typ string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"specversion": "1.0", "id": id, "type": typ, "source": "urn:logsafe",
		"subject": "subject" + crlfForged, "datacontenttype": "application/json",
		"data": map[string]any{"field": "value" + crlfForged},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
