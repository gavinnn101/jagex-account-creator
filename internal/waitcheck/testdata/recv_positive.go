package p

func TestA(t *testing.T) {
	ch := make(chan int)
	<-ch                      // want recv
	<-time.After(time.Second) // want recv
}
