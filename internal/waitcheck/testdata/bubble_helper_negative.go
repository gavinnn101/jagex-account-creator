package p

func inBubble(t *testing.T, f func(t *testing.T)) { synctest.Test(t, f) }

func TestA(t *testing.T) {
	inBubble(t, func(t *testing.T) { time.Sleep(time.Second) })
}
