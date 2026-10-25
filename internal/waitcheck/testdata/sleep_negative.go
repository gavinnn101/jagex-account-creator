package p

func TestA(t *testing.T) {
	synctest.Test(t, func(t *testing.T) { time.Sleep(time.Second) })
}
