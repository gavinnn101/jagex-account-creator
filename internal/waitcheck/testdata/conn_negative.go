package p

func dialWithDeadline() (net.Conn, error) {
	c, err := net.Dial("tcp", "x")
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	return c, err
}

func TestA(t *testing.T) {
	c, _ := net.Dial("tcp", "x")
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = c.Read(nil)
	g, _ := dialWithDeadline()
	_, _ = g.Read(nil)
}
