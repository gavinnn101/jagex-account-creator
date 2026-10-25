package p

func body(t *testing.T) {
	time.Sleep(time.Second)
}

func TestA(t *testing.T) {
	synctest.Test(t, body)
}
