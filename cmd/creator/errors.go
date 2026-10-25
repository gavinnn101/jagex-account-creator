package main

// markedError is err that also matches the sentinel mark. It keeps the text of
// err, so the mark changes no log line, and errors.Is still finds the sentinels of err.
type markedError struct {
	err  error
	mark error
}

func (e markedError) Error() string { return e.err.Error() }

func (e markedError) Unwrap() error { return e.err }

// Is matches mark.
func (e markedError) Is(target error) bool { return target == e.mark }
