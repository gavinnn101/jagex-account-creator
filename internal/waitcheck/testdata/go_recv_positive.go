package p

func TestA(t *testing.T) {
	done := make(chan struct{})
	go func() { <-done }() // want go-recv
	close(done)
}
