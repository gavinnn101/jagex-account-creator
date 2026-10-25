package p

func TestA(t *testing.T) {
	wait := func() {
		time.Sleep(time.Second) // want sleep
	}
	synctest.Test(t, func(t *testing.T) { wait() })
	wait()
}
