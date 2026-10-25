package p

func TestA(t *testing.T) {
	synctest.Test(t, func(t *testing.T) { time.NewTicker(time.Second).Stop() })
}
