package http

import "net/http"

// replayClass is what the Idempotency-Key middleware does with the answer of a
// problem type (ADR 0016). It is declared on the entry of the problem
// catalogue (problems.go); the middleware reads it and holds no list of slugs.
// A problem type without a class fails TestEveryProblemTypeDeclaresAReplayClass.
type replayClass uint8

const (
	// replayUnset is the zero value: no class. No problem type may have it; an
	// error the catalogue does not know (an unmapped 5xx) has it.
	replayUnset replayClass = iota
	// replayFinal: the answer cannot change for this request. The refusal is
	// stored under the key and replayed; the handler's writes roll back.
	replayFinal
	// replayOutcome: the command committed state and then refused, and the
	// refusal must be remembered. The writes commit together with the stored
	// answer (a FAILED feed answering negative-availability).
	replayOutcome
	// replayRetryable: nothing is remembered under the key and a retry executes
	// again. Normally the whole request rolls back; an entry with record set
	// keeps the one fact the use case recorded (see catalogueEntry).
	replayRetryable
)

func (c replayClass) String() string {
	switch c {
	case replayFinal:
		return "FINAL"
	case replayOutcome:
		return "OUTCOME"
	case replayRetryable:
		return "RETRYABLE"
	}
	return "UNSET"
}

// replayMarker is implemented by the middleware's capturing writer: writeError
// tells it the class of the problem it is about to write.
type replayMarker interface {
	MarkReplay(class replayClass, record bool)
}

// settlement is what the middleware does with a finished request.
type settlement uint8

const (
	// settleRelease rolls the whole request back, key row included: the key is
	// free again and a retry executes (5xx, panic, RETRYABLE, unclassified 4xx).
	settleRelease settlement = iota
	// settleStore commits the handler's writes and stores the answer (2xx).
	settleStore
	// settleFinal rolls the handler's writes back and stores the answer.
	settleFinal
	// settleOutcome commits the handler's writes and stores the answer.
	settleOutcome
	// settleRecord commits the handler's writes and frees the key: the answer is
	// not stored, the recorded fact stays.
	settleRecord
)

// settlementOf decides from the captured answer. Only 2xx and 5xx are status
// ranges here; every other answer is decided by the class its problem declares,
// and one with no class is released like RETRYABLE (the safe fallback).
func settlementOf(code int, class replayClass, record bool) settlement {
	switch {
	case code >= http.StatusInternalServerError:
		return settleRelease
	case code >= http.StatusOK && code < http.StatusMultipleChoices:
		return settleStore
	}
	switch class {
	case replayFinal:
		return settleFinal
	case replayOutcome:
		return settleOutcome
	case replayRetryable:
		if record {
			return settleRecord
		}
	}
	return settleRelease
}
