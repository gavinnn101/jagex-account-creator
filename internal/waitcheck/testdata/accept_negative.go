package p

func TestA(t *testing.T) {
	ln, _ := net.ListenTCP("tcp", nil)
	_ = ln.SetDeadline(time.Now().Add(5 * time.Second))
	c, _ := ln.Accept()
	_ = c
}
