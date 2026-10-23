//go:build integration

package main

import (
	"os"
	"testing"

	"github.com/claudioed/offer-availability/internal/testing/kafkatest"
	"github.com/claudioed/offer-availability/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	kafkatest.Shutdown()
	pgtest.Shutdown()
	os.Exit(code)
}
