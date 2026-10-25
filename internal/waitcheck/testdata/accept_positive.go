package p

func TestA(t *testing.T) {
	ln, _ := net.ListenTCP("tcp", nil)
	c, _ := ln.Accept() // want accept
	_ = c
}
