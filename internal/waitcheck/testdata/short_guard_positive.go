package p

func TestA(t *testing.T) {
	ch := make(chan int)
	select {
	case <-ch:
	case <-time.After(time.Second): // want short-guard
	}
}
