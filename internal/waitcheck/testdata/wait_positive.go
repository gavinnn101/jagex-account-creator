package p

type proxy struct{ wg sync.WaitGroup }

func (p *proxy) stop() {
	p.wg.Wait() // want wait
}

func drain(wg *sync.WaitGroup) {
	wg.Wait() // want wait
}

func TestA(t *testing.T) {
	var wg sync.WaitGroup
	wg.Wait() // want wait
	p := &proxy{}
	t.Cleanup(p.stop)
	drain(&wg)
	g := new(sync.WaitGroup)
	g.Wait() // want wait
}
