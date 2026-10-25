package p

func TestA(t *testing.T) {
	ch := make(chan int)
	select { // want select
	case <-ch:
	}
}
