package p

func wait() { time.Sleep(time.Second) }

func TestA(t *testing.T) {
	var g errgroup.Group
	go func() { wait() }()
	g.Go(func() error { wait(); return nil })
	_ = g.Wait()
}
