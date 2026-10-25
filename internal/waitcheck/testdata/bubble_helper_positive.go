package p

func direct(t *testing.T, f func(t *testing.T)) { f(t) }

func TestA(t *testing.T) {
	direct(t, func(t *testing.T) {
		time.Sleep(time.Second) // want sleep
	})
}
