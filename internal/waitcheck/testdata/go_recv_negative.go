package p

func TestA(t *testing.T) {
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}()
	close(done)
}
