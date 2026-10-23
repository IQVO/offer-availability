package kafka

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// fakeLister is a BrokerLister double: it answers without dialling.
type fakeLister struct {
	brokers []kafkago.Broker
	err     error
}

func (f fakeLister) Brokers(context.Context) ([]kafkago.Broker, error) {
	return f.brokers, f.err
}

func TestBrokerCheckPassesWhenABrokerAnswers(t *testing.T) {
	c := NewBrokerCheckWithLister(fakeLister{brokers: []kafkago.Broker{{Host: "b1", Port: 9092}}})
	if err := c.Check(context.Background()); err != nil {
		t.Fatalf("check = %v, want nil", err)
	}
}

func TestBrokerCheckFailsWhenMetadataFails(t *testing.T) {
	c := NewBrokerCheckWithLister(fakeLister{err: errors.New("connection refused")})
	err := c.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "kafka metadata:") {
		t.Fatalf("check = %v, want a wrapped kafka metadata error", err)
	}
}

func TestBrokerCheckFailsWhenNoBrokerIsListed(t *testing.T) {
	c := NewBrokerCheckWithLister(fakeLister{brokers: []kafkago.Broker{}})
	err := c.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "without listing any broker") {
		t.Fatalf("check = %v, want the empty-broker-list error", err)
	}
}

func TestNewBrokerCheckBuildsWithoutDialling(t *testing.T) {
	// Constructing the check must not touch the network: it only fails (or
	// succeeds) once Check asks a broker.
	if c := NewBrokerCheck([]string{"b1:9092", "b2:9092"}); c == nil || c.lister == nil {
		t.Fatalf("NewBrokerCheck = %+v, want a check over a dial lister", c)
	}
}

func TestDialListerWithoutAnAddressIsAnError(t *testing.T) {
	_, err := (dialLister{dialer: &kafkago.Dialer{Timeout: dialTimeout}}).Brokers(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no broker address configured") {
		t.Fatalf("Brokers = %v, want the no-address error", err)
	}
}

func TestDialListerReportsADialFailure(t *testing.T) {
	// Port 1 on loopback refuses at once: the dial-error branch of ask, no
	// broker needed. The error must name the address it could not reach.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d := dialLister{brokers: []string{"127.0.0.1:1"}, dialer: &kafkago.Dialer{Timeout: dialTimeout}}
	_, err := d.Brokers(ctx)
	if err == nil || !strings.Contains(err.Error(), "dial 127.0.0.1:1") {
		t.Fatalf("Brokers = %v, want a dial error naming the address", err)
	}
}
