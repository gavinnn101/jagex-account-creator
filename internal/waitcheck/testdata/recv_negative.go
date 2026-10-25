package p

func TestA(t *testing.T) {
	ch := make(chan int)
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("no value")
	}
}
