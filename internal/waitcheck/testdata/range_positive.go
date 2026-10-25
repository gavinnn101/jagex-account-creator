package p

func TestA(t *testing.T) {
	ch := make(chan int)
	for range ch { // want range
	}
}
