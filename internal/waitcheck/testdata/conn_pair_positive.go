package p

func pair() (net.Conn, net.Conn) {
	c, _ := net.Dial("tcp", "x")
	s, _ := net.Dial("tcp", "x")
	return c, s
}

func TestA(t *testing.T) {
	c, s := pair() // want conn
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = s.Read(nil)
}
