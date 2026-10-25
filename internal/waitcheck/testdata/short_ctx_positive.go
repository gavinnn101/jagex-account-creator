package p

func TestA(t *testing.T) {
	_, c1 := context.WithTimeout(t.Context(), 500*time.Millisecond)         // want short-ctx
	_, c2 := context.WithDeadline(t.Context(), time.Now().Add(time.Second)) // want short-ctx
	c1()
	c2()
}
