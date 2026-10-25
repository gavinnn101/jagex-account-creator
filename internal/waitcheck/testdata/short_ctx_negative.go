package p

func TestA(t *testing.T) {
	_, c1 := context.WithTimeout(t.Context(), 2*time.Second)
	_, c2 := context.WithDeadline(t.Context(), time.Now().Add(time.Duration(3)*time.Second))
	c1()
	c2()
}
