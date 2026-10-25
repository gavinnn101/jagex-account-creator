package p

type dialer struct{}

func (d *dialer) dial() net.Conn {
	a, _ := net.Pipe()
	return a
}

func pipePair() (net.Conn, net.Conn) { return net.Pipe() }

func TestA(t *testing.T) {
	a, b := net.Pipe() // want pipe
	c, d := pipePair() // want pipe
	var x dialer
	p := struct{ dialFn func() net.Conn }{dialFn: x.dial} // want pipe
	_, _, _, _, _ = a, b, c, d, p
}
