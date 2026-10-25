package p

func TestA(t *testing.T) {
	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the workers did not stop")
	}
	synctest.Test(t, func(t *testing.T) {
		var g sync.WaitGroup
		g.Wait()
	})
	var cmd exec.Cmd
	_ = cmd.Wait()
}
