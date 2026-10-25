package p

func wait() {
	time.Sleep(time.Second) // want sleep
}

func TestA(t *testing.T) {
	t.Run("x", func(t *testing.T) { wait() })
}
