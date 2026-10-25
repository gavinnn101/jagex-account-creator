package p

func body(t *testing.T) {
	time.Sleep(time.Second) // want sleep
}

func TestA(t *testing.T) {
	synctest.Test(t, body)
	body(t)
}
