package p

const guard = 5 * time.Second

func TestA(t *testing.T) {
	ch := make(chan int)
	var tm *time.Timer
	ctx, cancel := context.WithTimeout(t.Context(), guard)
	defer cancel()
	select {
	case <-ch:
	case <-tm.C:
	case <-ctx.Done():
	}
}
