package p

func TestA(t *testing.T) {
	tm := time.NewTimer(time.Second) // want timer
	tm.Stop()
}
